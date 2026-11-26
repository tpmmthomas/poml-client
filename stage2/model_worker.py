#!/usr/bin/env python3
"""Stage 2 model worker for the existing PoML Unix-socket protocol.

The worker supports three profile backends.

* ``dummy`` returns deterministic bytes and emits an explicitly insecure random
  proof. This is the development default for Stage 2.
* ``diffusion-small`` runs a fixed four-step, 8x8 PyTorch denoiser with
  deterministic weights and prescribed per-step randomness.
* ``gemma`` loads a pinned Hugging Face causal language model and produces a
  fixed-length sampled token sequence from the query prompt.

The worker does not claim to generate a zero-knowledge proof. The random proof
mode is accepted only when the Go chain profile says ``dummy-random-v1``.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import secrets
import socket
import sys
import threading
import time
from pathlib import Path
from typing import Any

# The worker is launched by absolute path from scripts/run_devnet.sh. Add the
# repository root explicitly so the checked-in protobuf module is importable.
REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
if str(REPOSITORY_ROOT) not in sys.path:
    sys.path.insert(0, str(REPOSITORY_ROOT))

from api.proto import poml_pb2 as pb


MAX_FRAME_BYTES = 16 * 1024 * 1024
PROTOCOL_VERSION = 1
_TORCH_THREADS_CONFIGURED = False


def hash_bytes(domain: str, *parts: bytes) -> bytes:
    """Match the tagged, length-delimited hash used by the Go client."""

    digest = hashlib.sha256()
    digest.update(b"poml-stage1/v1/" + domain.encode("utf-8") + b"/")
    for part in parts:
        digest.update(len(part).to_bytes(8, "big"))
        digest.update(part)
    return digest.digest()


def profile_digest(profile: dict[str, Any]) -> bytes:
    """Compute ``profiles.Spec.Digest`` without duplicating JSON ordering."""

    numbers = b"".join(
        int(value).to_bytes(8, "big")
        for value in (
            profile["randomness_slots"],
            profile["randomness_bytes"],
            profile["output_bytes"],
            profile.get("steps", 0),
            profile.get("max_tokens", 0),
            profile["ticket_weight"],
            profile["max_runtime_ms"],
        )
    )
    backend = profile.get("backend", "dummy")
    model_ref = profile.get("model_ref") or profile["id"]
    return hash_bytes(
        "worker-profile",
        profile["id"].encode(),
        backend.encode(),
        model_ref.encode(),
        profile["model_digest"].encode(),
        profile["certificate_profile"].encode(),
        numbers,
    )


def read_frame(connection: socket.socket) -> bytes:
    """Read one length-delimited protobuf frame."""

    header = read_exact(connection, 4)
    size = int.from_bytes(header, "big")
    if size <= 0 or size > MAX_FRAME_BYTES:
        raise ValueError("worker frame exceeds the 16 MiB limit")
    return read_exact(connection, size)


def read_exact(connection: socket.socket, size: int) -> bytes:
    data = bytearray()
    while len(data) < size:
        chunk = connection.recv(size - len(data))
        if not chunk:
            raise ConnectionError("worker connection closed")
        data.extend(chunk)
    return bytes(data)


def write_frame(connection: socket.socket, payload: bytes) -> None:
    if len(payload) == 0 or len(payload) > MAX_FRAME_BYTES:
        raise ValueError("worker frame has an invalid size")
    connection.sendall(len(payload).to_bytes(4, "big") + payload)


def configure_runtime(threads: int, cpu_start: int) -> None:
    """Keep a worker inside the caller's CPU budget."""

    logical = os.cpu_count() or 1
    limit = max(1, logical // 2)
    threads = max(1, min(threads or limit, limit))
    os.environ.setdefault("OMP_NUM_THREADS", str(threads))
    os.environ.setdefault("MKL_NUM_THREADS", str(threads))
    os.environ.setdefault("OPENBLAS_NUM_THREADS", str(threads))
    try:
        available = sorted(os.sched_getaffinity(0))
        allowed = available[cpu_start : cpu_start + threads]
        if allowed:
            os.sched_setaffinity(0, allowed)
    except AttributeError:
        pass

    # Torch is imported lazily so the dummy backend stays lightweight.
    global _TORCH_THREADS_CONFIGURED
    if "torch" in sys.modules and not _TORCH_THREADS_CONFIGURED:
        import torch

        torch.set_num_threads(threads)
        try:
            torch.set_num_interop_threads(1)
        except RuntimeError:
            # A prior test or library import may have started a Torch pool.
            pass
        torch.use_deterministic_algorithms(True)
        _TORCH_THREADS_CONFIGURED = True


class TinyDenoiser:
    """Small fixed U-Net-like denoiser used for protocol experiments."""

    def __init__(self, model_digest: bytes, steps: int, device: str, threads: int, cpu_start: int):
        import torch
        from torch import nn

        configure_runtime(threads, cpu_start)
        torch.manual_seed(int.from_bytes(model_digest[:8], "big"))

        class Network(nn.Module):
            def __init__(self) -> None:
                super().__init__()
                self.conv1 = nn.Conv2d(1, 8, 3, padding=1)
                self.conv2 = nn.Conv2d(8, 8, 3, padding=1)
                self.conv3 = nn.Conv2d(8, 1, 3, padding=1)
                self.condition = nn.Linear(4, 8)
                self.timestep = nn.Linear(4, 8)

            def forward(self, x, condition, timestep):
                bias = self.condition(condition) + self.timestep(timestep)
                bias = bias[:, :, None, None]
                x = torch.tanh(self.conv1(x) + bias)
                x = torch.tanh(self.conv2(x))
                return self.conv3(x)

        self.torch = torch
        self.network = Network().eval()
        self.network.to(device)
        self.steps = steps
        self.device = device
        self.parameter_count = sum(parameter.numel() for parameter in self.network.parameters())

    def _noise(self, slot: bytes):
        seed = int.from_bytes(hash_bytes("diffusion-noise", slot)[:8], "big")
        generator = self.torch.Generator(device="cpu")
        generator.manual_seed(seed)
        return self.torch.randn((1, 1, 8, 8), generator=generator, dtype=self.torch.float32)

    def infer(self, input_bytes: bytes, randomness: list[bytes]) -> tuple[bytes, int, list[int]]:
        if len(randomness) != self.steps + 1:
            raise ValueError("diffusion profile needs one initial and one noise slot per step")
        torch = self.torch
        condition_bytes = hashlib.sha256(b"poml-diffusion-condition/" + input_bytes).digest()[:16]
        condition = torch.tensor(
            [(int.from_bytes(condition_bytes[i : i + 4], "big") / 2**31) - 1 for i in range(0, 16, 4)],
            dtype=torch.float32,
        )[None, :]
        x = self._noise(randomness[0])
        condition = condition.to(self.device)
        x = x.to(self.device)
        for step in range(self.steps):
            timestep = torch.tensor(
                [[step / max(1, self.steps - 1)] * 4], dtype=torch.float32, device=self.device
            )
            predicted = self.network(x, condition, timestep)
            step_noise = self._noise(randomness[step + 1]).to(self.device)
            sigma = 0.12 / (step + 1)
            x = 0.82 * x - 0.18 * predicted + sigma * step_noise
        output = torch.clamp(torch.round((torch.tanh(x.cpu()) + 1.0) * 127.5), 0, 255)
        return output.to(torch.uint8).numpy().tobytes(), self.parameter_count * self.steps, [1, 8, 8]


class GemmaRunner:
    """Pinned, bounded Gemma generation adapter.

    The default model reference is read from the profile. A local directory can
    be supplied with ``--model-ref`` for offline tests or a private checkpoint.
    """

    def __init__(self, model_ref: str, max_tokens: int, device: str, threads: int, cpu_start: int):
        import torch

        # Import Torch before configuring it so both intra-op and inter-op
        # pools are explicitly bounded. The diffusion constructor follows the
        # same order; keeping this here avoids relying on environment defaults.
        configure_runtime(threads, cpu_start)
        from transformers import AutoModelForCausalLM, AutoTokenizer

        self.torch = torch
        self.device = device
        self.max_tokens = max_tokens
        self.tokenizer = AutoTokenizer.from_pretrained(model_ref, local_files_only=False)
        dtype = torch.float16 if device.startswith("cuda") else torch.float32
        self.model = AutoModelForCausalLM.from_pretrained(
            model_ref,
            dtype=dtype,
            low_cpu_mem_usage=True,
        )
        self.model.to(device)
        self.model.eval()
        self.parameter_count = sum(parameter.numel() for parameter in self.model.parameters())

    def infer(self, input_bytes: bytes, randomness: list[bytes]) -> tuple[bytes, int, list[int]]:
        if len(randomness) != 1:
            raise ValueError("Gemma profile needs one randomness slot")
        prompt = input_bytes.decode("utf-8", errors="strict")
        encoded = self.tokenizer(prompt, return_tensors="pt", truncation=True, max_length=512)
        input_ids = encoded["input_ids"].to(self.device)
        attention_mask = encoded["attention_mask"].to(self.device)
        seed = int.from_bytes(hash_bytes("gemma-seed", randomness[0])[:8], "big")
        generator = self.torch.Generator(device=self.device)
        generator.manual_seed(seed)
        generated: list[int] = []
        past = None
        with self.torch.inference_mode():
            for _ in range(self.max_tokens):
                kwargs = {"input_ids": input_ids, "use_cache": True}
                if past is not None:
                    kwargs["past_key_values"] = past
                    kwargs["attention_mask"] = attention_mask
                result = self.model(**kwargs)
                logits = result.logits[:, -1, :]
                probabilities = self.torch.softmax(logits.float() / 0.7, dim=-1)
                next_token = self.torch.multinomial(probabilities, 1, generator=generator)
                token = int(next_token.item())
                generated.append(token)
                past = result.past_key_values
                input_ids = next_token
                attention_mask = self.torch.cat(
                    [attention_mask, self.torch.ones((1, 1), dtype=attention_mask.dtype, device=self.device)], dim=1
                )
                if self.tokenizer.eos_token_id is not None and token == self.tokenizer.eos_token_id:
                    break
        output = b"".join(token.to_bytes(4, "big") for token in generated)
        # The Go worker boundary describes byte dimensions. The token count is
        # still recoverable as len(output) / 4 from this canonical encoding.
        return output, self.parameter_count * max(1, len(generated)), [len(output)]


class ModelWorker:
    """Socket server that dispatches one selected profile backend."""

    def __init__(self, profile: dict[str, Any], device: str, threads: int, cpu_start: int, model_ref: str | None):
        self.profile = profile
        self.device = device
        self.threads = threads
        self.cpu_start = cpu_start
        self.model_ref = model_ref or profile.get("model_ref", profile["id"])
        self.backend = profile.get("backend", "dummy")
        self.model: TinyDenoiser | GemmaRunner | None = None
        configure_runtime(threads, cpu_start)
        if self.backend == "diffusion-small":
            self.model = TinyDenoiser(
                bytes.fromhex(profile["model_digest"]), profile.get("steps", 4), device, threads, cpu_start
            )
        elif self.backend == "gemma":
            self.model = GemmaRunner(self.model_ref, profile.get("max_tokens", 16), device, threads, cpu_start)
        elif self.backend not in {"dummy", "fixture"}:
            raise ValueError(f"unsupported Python worker backend {self.backend!r}")
        # One model execution at a time keeps a worker's Torch thread pool
        # inside its CPU or GPU reservation. The Go miner still controls how
        # many attempts are in flight and records queue time at this boundary.
        self.slot = threading.BoundedSemaphore(1 if self.backend != "dummy" else max(1, threads))

    def run(self, request: pb.WorkerRequest) -> pb.WorkerResponse:
        response = pb.WorkerResponse(
            request_id=request.request_id,
            version=PROTOCOL_VERSION,
            profile_digest=request.profile_digest,
        )
        if request.version != PROTOCOL_VERSION or request.profile_id != self.profile["id"]:
            return self.failed(response, "worker version or profile ID mismatch")
        if request.profile_digest != profile_digest(self.profile):
            return self.failed(response, "worker profile digest mismatch")
        if request.certificate_profile != self.profile["certificate_profile"]:
            return self.failed(response, "worker proof profile mismatch")
        started = time.perf_counter_ns()
        cpu_started = time.thread_time_ns()
        try:
            with self.slot:
                if request.kind == pb.WorkerRequest.INFERENCE:
                    self.infer(request, response)
                elif request.kind == pb.WorkerRequest.CERTIFICATE:
                    self.prove(request, response)
                else:
                    raise ValueError("unknown worker request kind")
        except Exception as error:  # The response reports the boundary error to the Go caller.
            return self.failed(response, str(error), started, cpu_started)
        response.ok = True
        response.duration_ns = time.perf_counter_ns() - started
        response.cpu_ns = time.thread_time_ns() - cpu_started
        return response

    @staticmethod
    def failed(response: pb.WorkerResponse, message: str, started: int | None = None, cpu_started: int | None = None):
        response.error = message
        if started is not None:
            response.duration_ns = time.perf_counter_ns() - started
        if cpu_started is not None:
            response.cpu_ns = time.thread_time_ns() - cpu_started
        return response

    def validate_inference(self, request: pb.WorkerRequest) -> None:
        if request.model_digest != bytes.fromhex(self.profile["model_digest"]):
            raise ValueError("model digest mismatch")
        if not request.input or request.input_commitment != hash_bytes("input", request.input):
            raise ValueError("input commitment mismatch")
        if len(request.parent) != 32 or len(request.prefix_digest) != 32:
            raise ValueError("attempt context has invalid hash lengths")
        if not request.query_id or not request.attempt_id or not request.miner_id:
            raise ValueError("attempt metadata is incomplete")
        expected_slots = int(self.profile["randomness_slots"])
        expected_bytes = int(self.profile["randomness_bytes"])
        if len(request.randomness) != expected_slots:
            raise ValueError(f"expected {expected_slots} randomness slots")
        if any(len(slot) != expected_bytes for slot in request.randomness):
            raise ValueError(f"randomness slots must contain {expected_bytes} bytes")

    def infer(self, request: pb.WorkerRequest, response: pb.WorkerResponse) -> None:
        self.validate_inference(request)
        randomness = list(request.randomness)
        if self.backend in {"dummy", "fixture"}:
            output = expand_bytes(
                int(self.profile["output_bytes"]),
                "dummy-output",
                bytes.fromhex(self.profile["model_digest"]),
                request.input,
                *randomness,
            )
            operations = len(request.input) + sum(len(slot) for slot in randomness)
            shape = [len(output)]
        else:
            assert self.model is not None
            output, operations, shape = self.model.infer(request.input, randomness)
        if not output or len(output) > int(self.profile["output_bytes"]):
            raise ValueError("model output exceeds profile limit")
        response.output = output
        response.output_shape.extend(shape)
        response.operation_count = int(operations)
        response.execution_witness = hash_bytes(
            "model-witness", bytes.fromhex(self.profile["model_digest"]), request.input, output
        )
        response.result_digest = hash_bytes("worker-output", output)

    @staticmethod
    def prove(request: pb.WorkerRequest, response: pb.WorkerResponse) -> None:
        if request.certificate_profile != "dummy-random-v1":
            raise ValueError("the Python worker only supports the explicitly dummy proof profile")
        if not request.statement or request.witness is None or not request.witness.output:
            raise ValueError("dummy proof request is missing statement or witness")
        response.certificate = secrets.token_bytes(64)
        response.result_digest = hash_bytes("worker-certificate", response.certificate)
        response.operation_count = len(response.certificate)


def expand_bytes(size: int, domain: str, *parts: bytes) -> bytes:
    output = bytearray()
    counter = 0
    while len(output) < size:
        output.extend(hash_bytes(domain, *parts, counter.to_bytes(4, "big")))
        counter += 1
    return bytes(output[:size])


def load_profile(path: Path, profile_id: str) -> dict[str, Any]:
    profile_path = path / f"{profile_id}.json"
    with profile_path.open("r", encoding="utf-8") as handle:
        profile = json.load(handle)
    if profile.get("id") != profile_id:
        raise ValueError(f"profile file {profile_path} has the wrong ID")
    return profile


def serve(worker: ModelWorker, socket_path: Path) -> None:
    socket_path.parent.mkdir(parents=True, exist_ok=True)
    if socket_path.exists():
        if not socket_path.is_socket():
            raise RuntimeError(f"worker path is not a socket: {socket_path}")
        try:
            probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            probe.settimeout(0.1)
            probe.connect(str(socket_path))
            probe.close()
            raise RuntimeError(f"worker socket is already listening: {socket_path}")
        except (ConnectionRefusedError, FileNotFoundError):
            socket_path.unlink()
    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    server.bind(str(socket_path))
    os.chmod(socket_path, 0o600)
    server.listen(16)
    print(f"model worker backend={worker.backend} profile={worker.profile['id']} socket={socket_path}", flush=True)
    try:
        while True:
            connection, _ = server.accept()
            threading.Thread(target=handle_connection, args=(worker, connection), daemon=True).start()
    finally:
        server.close()
        socket_path.unlink(missing_ok=True)


def handle_connection(worker: ModelWorker, connection: socket.socket) -> None:
    try:
        frame = read_frame(connection)
        envelope = pb.Envelope.FromString(frame)
        if envelope.kind != pb.Envelope.WORKER_REQUEST:
            raise ValueError("unexpected envelope kind")
        request = pb.WorkerRequest.FromString(envelope.payload)
        response = worker.run(request)
    except Exception as error:
        response = pb.WorkerResponse(version=PROTOCOL_VERSION, ok=False, error=str(error))
    try:
        payload = pb.Envelope(kind=pb.Envelope.WORKER_RESPONSE, payload=response.SerializeToString()).SerializeToString()
        write_frame(connection, payload)
    except (BrokenPipeError, ConnectionError):
        pass
    finally:
        connection.close()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--socket", required=True, type=Path)
    parser.add_argument("--profile-id", required=True)
    parser.add_argument("--profiles-dir", type=Path, default=Path("profiles"))
    parser.add_argument("--model-ref", default=None, help="override a Hugging Face model ID or local checkpoint path")
    parser.add_argument("--device", default="cpu", choices=["cpu", "cuda:0"], help="use CPU or the first GPU")
    parser.add_argument("--threads", type=int, default=0, help="CPU threads, capped at half the host")
    parser.add_argument("--cpu-start", type=int, default=0, help="first logical CPU in the worker's affinity slice")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    profile = load_profile(args.profiles_dir, args.profile_id)
    if profile.get("backend") == "fixture":
        raise SystemExit("fixture profiles must use cmd/pomlworker")
    worker = ModelWorker(profile, args.device, args.threads, args.cpu_start, args.model_ref)
    serve(worker, args.socket)


if __name__ == "__main__":
    main()
