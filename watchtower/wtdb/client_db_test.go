package wtdb_test

import (
	crand "crypto/rand"
	"io"
	"net"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/kvdb"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/watchtower/blob"
	"github.com/lightningnetwork/lnd/watchtower/wtclient"
	"github.com/lightningnetwork/lnd/watchtower/wtdb"
	"github.com/lightningnetwork/lnd/watchtower/wtmock"
	"github.com/lightningnetwork/lnd/watchtower/wtpolicy"
	"github.com/stretchr/testify/require"
)

// pseudoAddr is a fake network address to be used for testing purposes.
var pseudoAddr = &net.TCPAddr{IP: []byte{0x01, 0x00, 0x00, 0x00}, Port: 9911}

// clientDBInit is a closure used to initialize a wtclient.DB instance.
type clientDBInit func(t *testing.T) wtclient.DB

type clientDBHarness struct {
	t  *testing.T
	db wtclient.DB
}

func newClientDBHarness(t *testing.T, init clientDBInit) *clientDBHarness {
	db := init(t)

	h := &clientDBHarness{
		t:  t,
		db: db,
	}

	return h
}

func (h *clientDBHarness) insertSession(session *wtdb.ClientSession,
	expErr error) {

	h.t.Helper()

	err := h.db.CreateClientSession(session)
	require.ErrorIs(h.t, err, expErr)
}

func (h *clientDBHarness) listSessions(id *wtdb.TowerID,
	opts ...wtdb.ClientSessionListOption) map[wtdb.SessionID]*wtdb.ClientSession {

	h.t.Helper()

	sessions, err := h.db.ListClientSessions(id, opts...)
	require.NoError(h.t, err, "unable to list client sessions")

	return sessions
}

func (h *clientDBHarness) nextKeyIndex(id wtdb.TowerID,
	blobType blob.Type) uint32 {

	h.t.Helper()

	index, err := h.db.NextSessionKeyIndex(id, blobType)
	require.NoError(h.t, err, "unable to create next session key index")
	require.NotZero(h.t, index, "next key index should never be 0")

	return index
}

func (h *clientDBHarness) createTower(lnAddr *lnwire.NetAddress,
	expErr error) *wtdb.Tower {

	h.t.Helper()

	tower, err := h.db.CreateTower(lnAddr)
	require.ErrorIs(h.t, err, expErr)
	require.NotZero(h.t, tower.ID, "tower id should never be 0")

	for _, session := range h.listSessions(&tower.ID) {
		require.Equal(h.t, wtdb.CSessionActive, session.Status)
	}

	return tower
}

func (h *clientDBHarness) removeTower(pubKey *btcec.PublicKey, addr net.Addr,
	hasSessions bool, expErr error) {

	h.t.Helper()

	err := h.db.RemoveTower(pubKey, addr)
	require.ErrorIs(h.t, err, expErr)

	if expErr != nil {
		return
	}

	pubKeyStr := pubKey.SerializeCompressed()

	if addr != nil {
		tower, err := h.db.LoadTower(pubKey)
		require.NoErrorf(h.t, err, "expected tower %x to still exist",
			pubKeyStr)

		removedAddr := addr.String()
		for _, towerAddr := range tower.Addresses {
			require.NotEqualf(h.t, removedAddr, towerAddr,
				"address %v not removed for tower %x",
				removedAddr, pubKeyStr)
		}
	} else {
		tower, err := h.db.LoadTower(pubKey)
		if hasSessions {
			require.NoError(h.t, err, "expected tower %x with "+
				"sessions to still exist", pubKeyStr)
		} else {
			require.Errorf(h.t, err, "expected tower %x with no "+
				"sessions to not exist", pubKeyStr)
			return
		}

		for _, session := range h.listSessions(&tower.ID) {
			require.Equal(h.t, wtdb.CSessionInactive,
				session.Status, "expected status for session "+
					"%v to be %v, got %v", session.ID,
				wtdb.CSessionInactive, session.Status)
		}
	}
}

func (h *clientDBHarness) loadTower(pubKey *btcec.PublicKey,
	expErr error) *wtdb.Tower {

	h.t.Helper()

	tower, err := h.db.LoadTower(pubKey)
	require.ErrorIs(h.t, err, expErr)

	return tower
}

func (h *clientDBHarness) loadTowerByID(id wtdb.TowerID,
	expErr error) *wtdb.Tower {

	h.t.Helper()

	tower, err := h.db.LoadTowerByID(id)
	require.ErrorIs(h.t, err, expErr)

	return tower
}

func (h *clientDBHarness) fetchChanSummaries() map[lnwire.ChannelID]wtdb.ClientChanSummary {
	h.t.Helper()

	summaries, err := h.db.FetchChanSummaries()
	require.NoError(h.t, err)

	return summaries
}

func (h *clientDBHarness) registerChan(chanID lnwire.ChannelID,
	sweepPkScript []byte, expErr error) {

	h.t.Helper()

	err := h.db.RegisterChannel(chanID, sweepPkScript)
	require.ErrorIs(h.t, err, expErr)
}

func (h *clientDBHarness) commitUpdate(id *wtdb.SessionID,
	update *wtdb.CommittedUpdate, expErr error) uint16 {

	h.t.Helper()

	lastApplied, err := h.db.CommitUpdate(id, update)
	require.ErrorIs(h.t, err, expErr)

	return lastApplied
}

func (h *clientDBHarness) ackUpdate(id *wtdb.SessionID, seqNum uint16,
	lastApplied uint16, expErr error) {

	h.t.Helper()

	err := h.db.AckUpdate(id, seqNum, lastApplied)
	require.ErrorIs(h.t, err, expErr)
}

// newTower is a helper function that creates a new tower with a randomly
// generated public key and inserts it into the client DB.
func (h *clientDBHarness) newTower() *wtdb.Tower {
	h.t.Helper()

	pk, err := randPubKey()
	require.NoError(h.t, err)

	// Insert a random tower into the database.
	return h.createTower(&lnwire.NetAddress{
		IdentityKey: pk,
		Address:     pseudoAddr,
	}, nil)
}

func (h *clientDBHarness) fetchSessionCommittedUpdates(id *wtdb.SessionID,
	expErr error) []wtdb.CommittedUpdate {

	h.t.Helper()

	updates, err := h.db.FetchSessionCommittedUpdates(id)
	if err != expErr {
		h.t.Fatalf("expected fetch session committed updates error: "+
			"%v, got: %v", expErr, err)
	}

	return updates
}

// testCreateClientSession asserts various conditions regarding the creation of
// a new ClientSession. The test asserts:
//   - client sessions can only be created if a session key index is reserved.
//   - client sessions cannot be created with an incorrect session key index .
//   - inserting duplicate sessions fails.
func testCreateClientSession(h *clientDBHarness) {
	const blobType = blob.TypeAltruistAnchorCommit

	tower := h.newTower()

	// Create a test client session to insert.
	session := &wtdb.ClientSession{
		ClientSessionBody: wtdb.ClientSessionBody{
			TowerID: tower.ID,
			Policy: wtpolicy.Policy{
				TxPolicy: wtpolicy.TxPolicy{
					BlobType: blobType,
				},
				MaxUpdates: 100,
			},
			RewardPkScript: []byte{0x01, 0x02, 0x03},
		},
		ID: wtdb.SessionID([33]byte{0x01}),
	}

	// First, assert that this session is not already present in the
	// database.
	_, ok := h.listSessions(nil)[session.ID]
	require.Falsef(h.t, ok, "session for id %x should not exist yet",
		session.ID)

	// Attempting to insert the client session without reserving a session
	// key index should fail.
	h.insertSession(session, wtdb.ErrNoReservedKeyIndex)

	// Now, reserve a session key for this tower.
	keyIndex := h.nextKeyIndex(session.TowerID, blobType)

	// The client session hasn't been updated with the reserved key index
	// (since it's still zero). Inserting should fail due to the mismatch.
	h.insertSession(session, wtdb.ErrIncorrectKeyIndex)

	// Reserve another key for the same index. Since no session has been
	// successfully created, it should return the same index to maintain
	// idempotency across restarts.
	keyIndex2 := h.nextKeyIndex(session.TowerID, blobType)
	require.Equalf(h.t, keyIndex, keyIndex2, "next key index should "+
		"be idempotent: want: %v, got %v", keyIndex, keyIndex2)

	// Now, set the client session's key index so that it is proper and
	// insert it. This should succeed.
	session.KeyIndex = keyIndex
	h.insertSession(session, nil)

	// Verify that the session now exists in the database.
	_, ok = h.listSessions(nil)[session.ID]
	require.Truef(h.t, ok, "session for id %x should exist now", session.ID)

	// Attempt to insert the session again, which should fail due to the
	// session already existing.
	h.insertSession(session, wtdb.ErrClientSessionAlreadyExists)

	// Finally, assert that reserving another key index succeeds with a
	// different key index, now that the first one has been finalized.
	keyIndex3 := h.nextKeyIndex(session.TowerID, blobType)
	require.NotEqualf(h.t, keyIndex, keyIndex3, "key index still "+
		"reserved after creating session")
}

// testFilterClientSessions asserts that we can correctly filter client sessions
// for a specific tower.
func testFilterClientSessions(h *clientDBHarness) {
	// We'll create three client sessions, the first two belonging to one
	// tower, and the last belonging to another one.
	const numSessions = 3
	const blobType = blob.TypeAltruistCommit
	towerSessions := make(map[wtdb.TowerID][]wtdb.SessionID)
	for i := 0; i < numSessions; i++ {
		tower := h.newTower()
		keyIndex := h.nextKeyIndex(tower.ID, blobType)
		sessionID := wtdb.SessionID([33]byte{byte(i)})
		h.insertSession(&wtdb.ClientSession{
			ClientSessionBody: wtdb.ClientSessionBody{
				TowerID: tower.ID,
				Policy: wtpolicy.Policy{
					TxPolicy: wtpolicy.TxPolicy{
						BlobType: blobType,
					},
					MaxUpdates: 100,
				},
				RewardPkScript: []byte{0x01, 0x02, 0x03},
				KeyIndex:       keyIndex,
			},
			ID: sessionID,
		}, nil)
		towerSessions[tower.ID] = append(
			towerSessions[tower.ID], sessionID,
		)
	}

	// We should see the expected sessions for each tower when filtering
	// them.
	for towerID, expectedSessions := range towerSessions {
		sessions := h.listSessions(&towerID)
		require.Len(h.t, sessions, len(expectedSessions))

		for _, expectedSession := range expectedSessions {
			_, ok := sessions[expectedSession]
			require.Truef(h.t, ok, "expected session %v for "+
				"tower %v", expectedSession, towerID)
		}
	}
}

// testCreateTower asserts the behavior of creating new Tower objects within the
// database, and that the latest address is always prepended to the list of
// known addresses for the tower.
func testCreateTower(h *clientDBHarness) {
	// Test that loading a tower with an arbitrary tower id fails.
	h.loadTowerByID(20, wtdb.ErrTowerNotFound)

	tower := h.newTower()
	require.Len(h.t, tower.LNAddrs(), 1)
	towerAddr := tower.LNAddrs()[0]

	// Load the tower from the database and assert that it matches the tower
	// we created.
	tower2 := h.loadTowerByID(tower.ID, nil)
	require.Equal(h.t, tower, tower2)

	tower2 = h.loadTower(tower.IdentityKey, nil)
	require.Equal(h.t, tower, tower2)

	// Insert the address again into the database. Since the address is the
	// same, this should result in an unmodified tower record.
	towerDupAddr := h.createTower(towerAddr, nil)
	require.Lenf(h.t, towerDupAddr.Addresses, 1, "duplicate address "+
		"should be deduped")

	require.Equal(h.t, tower, towerDupAddr)

	// Generate a new address for this tower.
	addr2 := &net.TCPAddr{IP: []byte{0x02, 0x00, 0x00, 0x00}, Port: 9911}

	lnAddr2 := &lnwire.NetAddress{
		IdentityKey: tower.IdentityKey,
		Address:     addr2,
	}

	// Insert the updated address, which should produce a tower with a new
	// address.
	towerNewAddr := h.createTower(lnAddr2, nil)

	// Load the tower from the database, and assert that it matches the
	// tower returned from creation.
	towerNewAddr2 := h.loadTowerByID(tower.ID, nil)
	require.Equal(h.t, towerNewAddr, towerNewAddr2)

	towerNewAddr2 = h.loadTower(tower.IdentityKey, nil)
	require.Equal(h.t, towerNewAddr, towerNewAddr2)

	// Assert that there are now two addresses on the tower object.
	require.Lenf(h.t, towerNewAddr.Addresses, 2, "new address should be "+
		"added")

	// Finally, assert that the new address was prepended since it is deemed
	// fresher.
	require.Equal(h.t, tower.Addresses, towerNewAddr.Addresses[1:])
}

// testRemoveTower asserts the behavior of removing Tower objects as a whole and
// removing addresses from Tower objects within the database.
func testRemoveTower(h *clientDBHarness) {
	// Generate a random public key we'll use for our tower.
	pk, err := randPubKey()
	require.NoError(h.t, err)

	// Removing a tower that does not exist within the database should
	// result in a NOP.
	h.removeTower(pk, nil, false, nil)

	// We'll create a tower with two addresses.
	addr1 := &net.TCPAddr{IP: []byte{0x01, 0x00, 0x00, 0x00}, Port: 9911}
	addr2 := &net.TCPAddr{IP: []byte{0x02, 0x00, 0x00, 0x00}, Port: 9911}
	h.createTower(&lnwire.NetAddress{
		IdentityKey: pk,
		Address:     addr1,
	}, nil)
	h.createTower(&lnwire.NetAddress{
		IdentityKey: pk,
		Address:     addr2,
	}, nil)

	// We'll then remove the second address. We should now only see the
	// first.
	h.removeTower(pk, addr2, false, nil)

	// We'll then remove the first address. We should now see that the tower
	// has no addresses left.
	h.removeTower(pk, addr1, false, wtdb.ErrLastTowerAddr)

	// Removing the tower as a whole from the database should succeed since
	// there aren't any active sessions for it.
	h.removeTower(pk, nil, false, nil)

	// We'll then recreate the tower, but this time we'll create a session
	// for it.
	tower := h.createTower(&lnwire.NetAddress{
		IdentityKey: pk,
		Address:     addr1,
	}, nil)

	const blobType = blob.TypeAltruistCommit
	session := &wtdb.ClientSession{
		ClientSessionBody: wtdb.ClientSessionBody{
			TowerID: tower.ID,
			Policy: wtpolicy.Policy{
				TxPolicy: wtpolicy.TxPolicy{
					BlobType: blobType,
				},
				MaxUpdates: 100,
			},
			RewardPkScript: []byte{0x01, 0x02, 0x03},
			KeyIndex:       h.nextKeyIndex(tower.ID, blobType),
		},
		ID: wtdb.SessionID([33]byte{0x01}),
	}
	h.insertSession(session, nil)
	update := randCommittedUpdate(h.t, 1)
	h.commitUpdate(&session.ID, update, nil)

	// We should not be able to fully remove it from the database since
	// there's a session and it has unacked updates.
	h.removeTower(pk, nil, true, wtdb.ErrTowerUnackedUpdates)

	// Removing the tower after all sessions no longer have unacked updates
	// should result in the sessions becoming inactive.
	h.ackUpdate(&session.ID, 1, 1, nil)
	h.removeTower(pk, nil, true, nil)

	// Creating the tower again should mark all of the sessions active once
	// again.
	h.createTower(&lnwire.NetAddress{
		IdentityKey: pk,
		Address:     addr1,
	}, nil)
}

// testChanSummaries tests the process of a registering a channel and its
// associated sweep pkscript.
func testChanSummaries(h *clientDBHarness) {
	// First, assert that this channel is not already registered.
	var chanID lnwire.ChannelID
	_, ok := h.fetchChanSummaries()[chanID]
	require.Falsef(h.t, ok, "pkscript for channel %x should not exist yet",
		chanID)

	// Generate a random sweep pkscript and register it for this channel.
	expPkScript := make([]byte, 22)
	_, err := io.ReadFull(crand.Reader, expPkScript)
	require.NoError(h.t, err)

	h.registerChan(chanID, expPkScript, nil)

	// Assert that the channel exists and that its sweep pkscript matches
	// the one we registered.
	summary, ok := h.fetchChanSummaries()[chanID]
	require.Truef(h.t, ok, "pkscript for channel %x should not exist yet",
		chanID)
	require.Equal(h.t, expPkScript, summary.SweepPkScript)

	// Finally, assert that re-registering the same channel produces a
	// failure.
	h.registerChan(chanID, expPkScript, wtdb.ErrChannelAlreadyRegistered)
}

// testCommitUpdate tests the behavior of CommitUpdate, ensuring that they can
func testCommitUpdate(h *clientDBHarness) {
	const blobType = blob.TypeAltruistCommit

	tower := h.newTower()
	session := &wtdb.ClientSession{
		ClientSessionBody: wtdb.ClientSessionBody{
			TowerID: tower.ID,
			Policy: wtpolicy.Policy{
				TxPolicy: wtpolicy.TxPolicy{
					BlobType: blobType,
				},
				MaxUpdates: 100,
			},
			RewardPkScript: []byte{0x01, 0x02, 0x03},
		},
		ID: wtdb.SessionID([33]byte{0x02}),
	}

	// Generate a random update and try to commit before inserting the
	// session, which should fail.
	update1 := randCommittedUpdate(h.t, 1)
	h.commitUpdate(&session.ID, update1, wtdb.ErrClientSessionNotFound)
	h.fetchSessionCommittedUpdates(
		&session.ID, wtdb.ErrClientSessionNotFound,
	)

	// Reserve a session key index and insert the session.
	session.KeyIndex = h.nextKeyIndex(session.TowerID, blobType)
	h.insertSession(session, nil)

	// Now, try to commit the update that failed initially which should
	// succeed. The lastApplied value should be 0 since we have not received
	// an ack from the tower.
	lastApplied := h.commitUpdate(&session.ID, update1, nil)
	require.Zero(h.t, lastApplied)

	// Assert that the committed update appears in the client session's
	// CommittedUpdates map when loaded from disk and that there are no
	// AckedUpdates.
	h.assertUpdates(session.ID, []wtdb.CommittedUpdate{*update1}, nil)

	// Try to commit the same update, which should succeed due to
	// idempotency (which is preserved when the breach hint is identical to
	// the on-disk update's hint). The lastApplied value should remain
	// unchanged.
	lastApplied2 := h.commitUpdate(&session.ID, update1, nil)
	require.Equal(h.t, lastApplied, lastApplied2)

	// Assert that the loaded ClientSession is the same as before.
	h.assertUpdates(session.ID, []wtdb.CommittedUpdate{*update1}, nil)

	// Generate another random update and try to commit it at the identical
	// sequence number. Since the breach hint has changed, this should fail.
	update2 := randCommittedUpdate(h.t, 1)
	h.commitUpdate(&session.ID, update2, wtdb.ErrUpdateAlreadyCommitted)

	// Next, insert the new update at the next unallocated sequence number
	// which should succeed.
	update2.SeqNum = 2
	lastApplied3 := h.commitUpdate(&session.ID, update2, nil)
	require.Equal(h.t, lastApplied, lastApplied3)

	// Check that both updates now appear as committed on the ClientSession
	// loaded from disk.
	h.assertUpdates(session.ID, []wtdb.CommittedUpdate{
		*update1,
		*update2,
	}, nil)

	// Finally, create one more random update and try to commit it at index
	// 4, which should be rejected since 3 is the next slot the database
	// expects.
	update4 := randCommittedUpdate(h.t, 4)
	h.commitUpdate(&session.ID, update4, wtdb.ErrCommitUnorderedUpdate)

	// Assert that the ClientSession loaded from disk remains unchanged.
	h.assertUpdates(session.ID, []wtdb.CommittedUpdate{
		*update1,
		*update2,
	}, nil)
}

func perAckedUpdate(updates map[uint16]wtdb.BackupID) func(
	_ *wtdb.ClientSession, seq uint16, id wtdb.BackupID) {

	return func(_ *wtdb.ClientSession, seq uint16,
		id wtdb.BackupID) {

		updates[seq] = id
	}
}

// testAckUpdate asserts the behavior of AckUpdate.
func testAckUpdate(h *clientDBHarness) {
	const blobType = blob.TypeAltruistCommit

	tower := h.newTower()

	// Create a new session that the updates in this will be tied to.
	session := &wtdb.ClientSession{
		ClientSessionBody: wtdb.ClientSessionBody{
			TowerID: tower.ID,
			Policy: wtpolicy.Policy{
				TxPolicy: wtpolicy.TxPolicy{
					BlobType: blobType,
				},
				MaxUpdates: 100,
			},
			RewardPkScript: []byte{0x01, 0x02, 0x03},
		},
		ID: wtdb.SessionID([33]byte{0x03}),
	}

	// Try to ack an update before inserting the client session, which
	// should fail.
	h.ackUpdate(&session.ID, 1, 0, wtdb.ErrClientSessionNotFound)

	// Reserve a session key and insert the client session.
	session.KeyIndex = h.nextKeyIndex(session.TowerID, blobType)
	h.insertSession(session, nil)

	// Now, try to ack update 1. This should fail since update 1 was never
	// committed.
	h.ackUpdate(&session.ID, 1, 0, wtdb.ErrCommittedUpdateNotFound)

	// Commit to a random update at seqnum 1.
	update1 := randCommittedUpdate(h.t, 1)
	lastApplied := h.commitUpdate(&session.ID, update1, nil)
	require.Zero(h.t, lastApplied)

	// Acking seqnum 1 should succeed.
	h.ackUpdate(&session.ID, 1, 1, nil)

	// Acking seqnum 1 again should fail.
	h.ackUpdate(&session.ID, 1, 1, wtdb.ErrCommittedUpdateNotFound)

	// Acking a valid seqnum with a reverted last applied value should fail.
	h.ackUpdate(&session.ID, 1, 0, wtdb.ErrLastAppliedReversion)

	// Acking with a last applied greater than any allocated seqnum should
	// fail.
	h.ackUpdate(&session.ID, 4, 3, wtdb.ErrUnallocatedLastApplied)

	// Assert that the ClientSession loaded from disk has one update in it's
	// AckedUpdates map, and that the committed update has been removed.
	h.assertUpdates(session.ID, nil, map[uint16]wtdb.BackupID{
		1: update1.BackupID,
	})

	// Commit to another random update, and assert that the last applied
	// value is 1, since this was what was provided in the last successful
	// ack.
	update2 := randCommittedUpdate(h.t, 2)
	lastApplied = h.commitUpdate(&session.ID, update2, nil)
	require.EqualValues(h.t, 1, lastApplied)

	// Ack seqnum 2.
	h.ackUpdate(&session.ID, 2, 2, nil)

	// Assert that both updates exist as AckedUpdates when loaded from disk.
	h.assertUpdates(session.ID, nil, map[uint16]wtdb.BackupID{
		1: update1.BackupID,
		2: update2.BackupID,
	})

	// Acking again with a lower last applied should fail.
	h.ackUpdate(&session.ID, 2, 1, wtdb.ErrLastAppliedReversion)

	// Acking an unallocated seqnum should fail.
	h.ackUpdate(&session.ID, 4, 2, wtdb.ErrCommittedUpdateNotFound)

	// Acking with a last applied greater than any allocated seqnum should
	// fail.
	h.ackUpdate(&session.ID, 4, 3, wtdb.ErrUnallocatedLastApplied)
}

func (h *clientDBHarness) assertUpdates(id wtdb.SessionID,
	expectedPending []wtdb.CommittedUpdate,
	expectedAcked map[uint16]wtdb.BackupID) {

	ackedUpdates := make(map[uint16]wtdb.BackupID)
	_ = h.listSessions(
		nil, wtdb.WithPerAckedUpdate(perAckedUpdate(ackedUpdates)),
	)
	committedUpates := h.fetchSessionCommittedUpdates(&id, nil)
	checkCommittedUpdates(h.t, committedUpates, expectedPending)
	checkAckedUpdates(h.t, ackedUpdates, expectedAcked)
}

// checkCommittedUpdates asserts that the CommittedUpdates on session match the
// expUpdates provided.
func checkCommittedUpdates(t *testing.T, actualUpdates,
	expUpdates []wtdb.CommittedUpdate) {

	t.Helper()

	// We promote nil expUpdates to an initialized slice since the database
	// should never return a nil slice. This promotion is done purely out of
	// convenience for the testing framework.
	if expUpdates == nil {
		expUpdates = make([]wtdb.CommittedUpdate, 0)
	}

	require.Equal(t, expUpdates, actualUpdates)
}

// checkAckedUpdates asserts that the AckedUpdates on a session match the
// expUpdates provided.
func checkAckedUpdates(t *testing.T, actualUpdates,
	expUpdates map[uint16]wtdb.BackupID) {

	// We promote nil expUpdates to an initialized map since the database
	// should never return a nil map. This promotion is done purely out of
	// convenience for the testing framework.
	if expUpdates == nil {
		expUpdates = make(map[uint16]wtdb.BackupID)
	}

	require.Equal(t, expUpdates, actualUpdates)
}

// TestClientDB asserts the behavior of a fresh client db, a reopened client db,
// and the mock implementation. This ensures that all databases function
// identically, especially in the negative paths.
func TestClientDB(t *testing.T) {
	dbCfg := &kvdb.BoltConfig{DBTimeout: kvdb.DefaultDBTimeout}
	dbs := []struct {
		name string
		init clientDBInit
	}{
		{
			name: "fresh clientdb",
			init: func(t *testing.T) wtclient.DB {
				bdb, err := wtdb.NewBoltBackendCreator(
					true, t.TempDir(), "wtclient.db",
				)(dbCfg)
				require.NoError(t, err)

				db, err := wtdb.OpenClientDB(bdb)
				require.NoError(t, err)

				t.Cleanup(func() {
					db.Close()
				})

				return db
			},
		},
		{
			name: "reopened clientdb",
			init: func(t *testing.T) wtclient.DB {
				path := t.TempDir()

				bdb, err := wtdb.NewBoltBackendCreator(
					true, path, "wtclient.db",
				)(dbCfg)
				require.NoError(t, err)

				db, err := wtdb.OpenClientDB(bdb)
				require.NoError(t, err)
				db.Close()

				bdb, err = wtdb.NewBoltBackendCreator(
					true, path, "wtclient.db",
				)(dbCfg)
				require.NoError(t, err)

				db, err = wtdb.OpenClientDB(bdb)
				require.NoError(t, err)

				t.Cleanup(func() {
					db.Close()
				})

				return db
			},
		},
		{
			name: "mock",
			init: func(t *testing.T) wtclient.DB {
				return wtmock.NewClientDB()
			},
		},
	}

	tests := []struct {
		name string
		run  func(*clientDBHarness)
	}{
		{
			name: "create client session",
			run:  testCreateClientSession,
		},
		{
			name: "filter client sessions",
			run:  testFilterClientSessions,
		},
		{
			name: "create tower",
			run:  testCreateTower,
		},
		{
			name: "remove tower",
			run:  testRemoveTower,
		},
		{
			name: "chan summaries",
			run:  testChanSummaries,
		},
		{
			name: "commit update",
			run:  testCommitUpdate,
		},
		{
			name: "ack update",
			run:  testAckUpdate,
		},
	}

	for _, database := range dbs {
		db := database
		t.Run(db.name, func(t *testing.T) {
			t.Parallel()

			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					h := newClientDBHarness(t, db.init)

					test.run(h)
				})
			}
		})
	}
}

// randCommittedUpdate generates a random committed update.
func randCommittedUpdate(t *testing.T, seqNum uint16) *wtdb.CommittedUpdate {
	var chanID lnwire.ChannelID
	_, err := io.ReadFull(crand.Reader, chanID[:])
	require.NoError(t, err)

	var hint blob.BreachHint
	_, err = io.ReadFull(crand.Reader, hint[:])
	require.NoError(t, err)

	encBlob := make([]byte, blob.Size(blob.FlagCommitOutputs.Type()))
	_, err = io.ReadFull(crand.Reader, encBlob)
	require.NoError(t, err)

	return &wtdb.CommittedUpdate{
		SeqNum: seqNum,
		CommittedUpdateBody: wtdb.CommittedUpdateBody{
			BackupID: wtdb.BackupID{
				ChanID:       chanID,
				CommitHeight: 666,
			},
			Hint:          hint,
			EncryptedBlob: encBlob,
		},
	}
}
