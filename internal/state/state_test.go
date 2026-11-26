package state

import (
	"testing"

	"github.com/thomas/poml-node/internal/profile"
	"github.com/thomas/poml-node/internal/types"
)

func testGenesis() profile.Genesis {
	return profile.Genesis{
		Raw: profile.GenesisFile{
			ModelProfileID: "fixture-a", QueryFee: 20, ChallengeWindow: 2,
			AnswerWindow: 2, BondSlash: 10,
		},
		Accounts: map[string]types.Account{
			"user":       {Balance: 100},
			"miner":      {Balance: 100},
			"challenger": {Balance: 100},
		},
		Miners: map[string]types.Miner{
			"miner": {ID: "miner", Bond: 10, Active: true},
		},
	}
}

func queryTx(nonce uint64, id string, expiry uint64, fee uint64) types.Tx {
	input := []byte("payload-" + id)
	return types.Tx{Kind: types.TxQuery, Sender: "user", Nonce: nonce, QueryID: id,
		Input: input, Commitment: types.HashBytes("input", input), ModelID: "fixture-a", Expiry: expiry, Fee: fee}
}

func responseTx(nonce uint64, queryID, attempt string) types.Tx {
	return types.Tx{Kind: types.TxResponse, Sender: "miner", Nonce: nonce, QueryID: queryID,
		AttemptID: attempt, MinerID: "miner", Parent: types.HashBytes("parent", []byte("p")),
		PrefixDigest: types.HashBytes("prefix", []byte("p")), Ciphertext: []byte("cipher"),
		TicketHash: types.HashBytes("ticket", []byte(attempt)), CTicket: 1}
}

func TestResponseChallengeAnswerPaysOnceAndUnlocksBond(t *testing.T) {
	genesis := testGenesis()
	current := FromGenesis(genesis)
	if err := ApplyTransaction(current, queryTx(0, "q1", 20, 20), 1, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, responseTx(0, "q1", "a1"), 2, genesis); err != nil {
		t.Fatal(err)
	}
	if current.Accounts["miner"].Balance != 90 || current.Miners["miner"].LockedBond != 10 {
		t.Fatalf("response did not lock bond: account=%d locked=%d", current.Accounts["miner"].Balance, current.Miners["miner"].LockedBond)
	}
	challenge := types.Tx{Kind: types.TxChallenge, Sender: "challenger", Nonce: 0, QueryID: "q1", Fee: 3}
	if err := ApplyTransaction(current, challenge, 3, genesis); err != nil {
		t.Fatal(err)
	}
	answer := responseTx(0, "q1", "a1")
	answer.Kind = types.TxChallengeAnswer
	answer.ProofStatement = []byte("statement")
	answer.Proof = []byte("fixture-certificate")
	if err := ApplyTransaction(current, answer, 4, genesis); err != nil {
		t.Fatal(err)
	}
	if !current.Queries["q1"].Settled || current.Queries["q1"].Escrow != 0 {
		t.Fatal("answer did not settle query escrow")
	}
	if current.Accounts["miner"].Balance != 120 || current.Miners["miner"].LockedBond != 0 {
		t.Fatalf("answer did not pay and unlock exactly once: account=%d locked=%d", current.Accounts["miner"].Balance, current.Miners["miner"].LockedBond)
	}
	if current.Accounts["challenger"].Balance != 100 {
		t.Fatalf("valid answer did not refund challenge fee: %d", current.Accounts["challenger"].Balance)
	}
	if err := ApplyTransaction(current, answer, 5, genesis); err == nil {
		t.Fatal("accepted duplicate challenge answer")
	}
}

func TestPendingClaimSettlesWhenHeightAdvances(t *testing.T) {
	genesis := testGenesis()
	current := FromGenesis(genesis)
	if err := ApplyTransaction(current, queryTx(0, "q1", 20, 20), 1, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, responseTx(0, "q1", "a1"), 2, genesis); err != nil {
		t.Fatal(err)
	}
	AdvanceDeadlines(current, 5, genesis)
	if !current.Queries["q1"].Settled || current.Accounts["miner"].Balance != 120 || current.Miners["miner"].LockedBond != 0 {
		t.Fatalf("uncontested claim did not settle: query=%+v miner=%+v", current.Queries["q1"], current.Accounts["miner"])
	}
}

func TestMultipleResponsesFromOneMinerShareThePaymentNonce(t *testing.T) {
	genesis := testGenesis()
	current := FromGenesis(genesis)
	if err := ApplyTransaction(current, queryTx(0, "q1", 20, 20), 1, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, queryTx(1, "q2", 20, 20), 1, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, responseTx(0, "q1", "a1"), 2, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, responseTx(0, "q2", "a2"), 2, genesis); err != nil {
		t.Fatal(err)
	}
	if current.Accounts["miner"].Balance != 80 || current.Miners["miner"].LockedBond != 20 {
		t.Fatalf("responses should each lock a bond without consuming account nonces: account=%d locked=%d", current.Accounts["miner"].Balance, current.Miners["miner"].LockedBond)
	}
}

func TestChallengedClaimTimeoutRefundsAndSlashes(t *testing.T) {
	genesis := testGenesis()
	current := FromGenesis(genesis)
	if err := ApplyTransaction(current, queryTx(0, "q1", 20, 20), 1, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, responseTx(0, "q1", "a1"), 2, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, types.Tx{Kind: types.TxChallenge, Sender: "challenger", Nonce: 0, QueryID: "q1", Fee: 3}, 3, genesis); err != nil {
		t.Fatal(err)
	}
	AdvanceDeadlines(current, 6, genesis)
	if !current.Queries["q1"].Refunded || !current.Queries["q1"].Expired {
		t.Fatal("timed-out challenge did not refund query")
	}
	if current.Accounts["miner"].Balance != 90 || current.Miners["miner"].LockedBond != 0 {
		t.Fatalf("timed-out challenge did not slash bond: account=%d locked=%d", current.Accounts["miner"].Balance, current.Miners["miner"].LockedBond)
	}
	if current.Accounts["challenger"].Balance != 110 {
		t.Fatalf("challenger did not receive fee and slash: %d", current.Accounts["challenger"].Balance)
	}
}

func TestBalanceRaceIsResolvedByBlockOrder(t *testing.T) {
	genesis := testGenesis()
	genesis.Raw.QueryFee = 60
	current := FromGenesis(genesis)
	if err := ApplyTransaction(current, queryTx(0, "q1", 20, 60), 1, genesis); err != nil {
		t.Fatal(err)
	}
	if err := ApplyTransaction(current, queryTx(1, "q2", 20, 60), 1, genesis); err == nil {
		t.Fatal("accepted a second query after the balance was spent")
	}
}

func TestMinerRegistrationLocksAndRejectsDuplicate(t *testing.T) {
	genesis := testGenesis()
	current := FromGenesis(genesis)
	delete(current.Miners, "new-miner")
	tx := types.Tx{Kind: types.TxMinerRegistration, Sender: "user", Nonce: 0, MinerID: "new-miner", Amount: 20,
		IdentityPublic: make([]byte, 32), InferenceVRFPublic: make([]byte, 32), EncryptionVRFPublic: make([]byte, 32)}
	if err := ApplyTransaction(current, tx, 1, genesis); err != nil {
		t.Fatal(err)
	}
	if current.Accounts["user"].Balance != 80 || !current.Miners["new-miner"].Active {
		t.Fatal("registration did not lock the bond")
	}
	if err := ApplyTransaction(current, tx, 2, genesis); err == nil {
		t.Fatal("accepted duplicate miner registration")
	}
}
