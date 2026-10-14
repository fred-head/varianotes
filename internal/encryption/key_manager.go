package encryption

import (
	"crypto/subtle"
	"errors"
	"sync"
	"time"
	"unicode/utf8"
)

const DefaultIdleLockTimeout = 15 * time.Minute

var (
	ErrVaultLocked      = errors.New("vault is locked for this session")
	ErrSessionRequired  = errors.New("authenticated session is required")
	ErrVaultKeyMismatch = errors.New("vault is already unlocked with another key")
	ErrKeyManagerClosed = errors.New("encryption key manager is closed")
	ErrInvalidVaultID   = errors.New("invalid encryption vault ID")
)

type LockReason string

const (
	LockReasonInitial  LockReason = "initial"
	LockReasonIdle     LockReason = "idle"
	LockReasonManual   LockReason = "manual"
	LockReasonLogout   LockReason = "logout"
	LockReasonSession  LockReason = "session_change"
	LockReasonRestart  LockReason = "restart"
	LockReasonRevoked  LockReason = "session_revoked"
	LockReasonUnlocked LockReason = "unlocked"
)

// LockEvent contains no key material. Generations let tabs discard responses
// that were started under an earlier lock state.
type LockEvent struct {
	VaultID    string     `json:"vaultId"`
	Locked     bool       `json:"locked"`
	Generation uint64     `json:"generation"`
	Reason     LockReason `json:"reason"`
}

type keyGrant struct {
	generation uint64
}

type keySubscriber struct {
	session [32]byte
	channel chan LockEvent
}

type vaultKeyState struct {
	key          []byte
	grants       map[[32]byte]keyGrant
	generation   uint64
	lastActivity time.Time
	expiresAt    time.Time
	subscribers  map[uint64]keySubscriber
}

// KeyManager owns transient vault keys. It has no persistence path; a new
// manager always starts locked, and all grants are scoped to an authenticated
// session hash. Construct one manager per server process.
type KeyManager struct {
	// Publication callbacks never hold mu. Revocation waits for this barrier
	// without mu, so repository locks cannot deadlock status or idle locking.
	commitGate sync.RWMutex
	mu         sync.Mutex
	timeout    time.Duration
	vaults     map[string]*vaultKeyState
	timer      *time.Timer
	nextSubID  uint64
	closed     bool
}

func NewKeyManager() *KeyManager {
	return newKeyManager(DefaultIdleLockTimeout)
}

func newKeyManager(timeout time.Duration) *KeyManager {
	if timeout <= 0 {
		timeout = DefaultIdleLockTimeout
	}
	return &KeyManager{timeout: timeout, vaults: make(map[string]*vaultKeyState)}
}

// Unlock installs a key in process memory or grants the current session access
// to the already-unlocked vault when it presents the same key. A different
// session must call Unlock independently.
func (m *KeyManager) Unlock(vaultID string, sessionID, vaultKey []byte) error {
	if !validVaultID(vaultID) {
		return ErrInvalidVaultID
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return ErrSessionRequired
	}
	if len(vaultKey) != VaultKeySize {
		return ErrInvalidKey
	}

	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireWithCommitGateLocked(time.Now())
	if m.closed {
		return ErrKeyManagerClosed
	}
	state := m.stateLocked(vaultID)
	if len(state.key) == 0 {
		state.generation++
		state.key = append([]byte(nil), vaultKey...)
		state.grants = make(map[[32]byte]keyGrant)
		state.lastActivity = time.Now()
		state.expiresAt = state.lastActivity.Add(m.timeout)
		state.grants[session] = keyGrant{generation: state.generation}
		m.publishLocked(state, LockEvent{VaultID: vaultID, Locked: false, Generation: state.generation, Reason: LockReasonUnlocked}, &session)
		m.scheduleIdleTimerLocked()
		return nil
	}
	if len(state.key) != len(vaultKey) || subtle.ConstantTimeCompare(state.key, vaultKey) != 1 {
		return ErrVaultKeyMismatch
	}
	if _, exists := state.grants[session]; !exists {
		state.grants[session] = keyGrant{generation: state.generation}
		m.publishLocked(state, LockEvent{VaultID: vaultID, Locked: false, Generation: state.generation, Reason: LockReasonUnlocked}, &session)
	}
	state.lastActivity = time.Now()
	state.expiresAt = state.lastActivity.Add(m.timeout)
	m.scheduleIdleTimerLocked()
	return nil
}

// IsUnlocked reports whether this exact session has a live grant. It does not
// count as user activity and therefore never extends the idle deadline.
func (m *KeyManager) IsUnlocked(vaultID string, sessionID []byte) bool {
	if !validVaultID(vaultID) {
		return false
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(time.Now())
	state := m.vaults[vaultID]
	if state == nil || m.closed || len(state.key) != VaultKeySize {
		return false
	}
	grant, exists := state.grants[session]
	return exists && grant.generation == state.generation
}

// SessionStatus returns the lock state and generation visible to one session.
// It does not count as user activity and never extends the idle timeout.
func (m *KeyManager) SessionStatus(vaultID string, sessionID []byte) (bool, uint64, error) {
	if !validVaultID(vaultID) {
		return false, 0, ErrInvalidVaultID
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return false, 0, ErrSessionRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(time.Now())
	if m.closed {
		return false, 0, ErrKeyManagerClosed
	}
	state := m.stateLocked(vaultID)
	return hasGrant(state, session), state.generation, nil
}

// Touch records explicit foreground activity for a session with a live grant.
// Ordinary reads, writes, polling, and key use do not call this method.
func (m *KeyManager) Touch(vaultID string, sessionID []byte) error {
	if !validVaultID(vaultID) {
		return ErrInvalidVaultID
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return ErrSessionRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(time.Now())
	if m.closed {
		return ErrKeyManagerClosed
	}
	state := m.vaults[vaultID]
	if !hasGrant(state, session) {
		return ErrVaultLocked
	}
	state.lastActivity = time.Now()
	state.expiresAt = state.lastActivity.Add(m.timeout)
	m.scheduleIdleTimerLocked()
	return nil
}

// BeginUse creates a short-lived key lease for protected work. Call Close when
// finished. Results derived from the key must remain buffered until Commit
// succeeds; Commit serializes publication against lock transitions so a late
// response cannot become visible after a lock has completed.
func (m *KeyManager) BeginUse(vaultID string, sessionID []byte) (*KeyLease, error) {
	if !validVaultID(vaultID) {
		return nil, ErrInvalidVaultID
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return nil, ErrSessionRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(time.Now())
	if m.closed {
		return nil, ErrKeyManagerClosed
	}
	state := m.vaults[vaultID]
	if !hasGrant(state, session) {
		return nil, ErrVaultLocked
	}
	return &KeyLease{manager: m, vaultID: vaultID, session: session, generation: state.generation, key: append([]byte(nil), state.key...)}, nil
}

// KeyLease confines a copied key to one piece of protected work. UseKey reads
// and prepares buffered output/ciphertext stages; it never authorizes a live
// mutation. Durable mutations use GuardCommit and buffered responses use Commit.
type KeyLease struct {
	manager    *KeyManager
	vaultID    string
	session    [32]byte
	generation uint64
	key        []byte
	closed     bool
	mu         sync.Mutex
}

// Generation identifies the lock epoch captured by this lease. It contains no
// key material and is useful only together with the vault and session binding.
func (l *KeyLease) Generation() uint64 {
	if l == nil {
		return 0
	}
	return l.generation
}

// GenerationCurrent reports whether a session still has a grant from the
// supplied lock epoch. Callers may retain this non-secret binding without
// retaining a copied vault key between requests.
func (m *KeyManager) GenerationCurrent(vaultID string, sessionID []byte, generation uint64) bool {
	if m == nil || !validVaultID(vaultID) || generation == 0 {
		return false
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.expireLocked(time.Now())
	state := m.vaults[vaultID]
	if state == nil || len(state.key) != VaultKeySize || state.generation != generation {
		return false
	}
	grant, ok := state.grants[session]
	return ok && grant.generation == generation
}

// CheckCurrent reports whether this lease still belongs to the active session
// grant. Long running streams should call it between bounded chunks so a lock
// or logout stops further publication.
func (l *KeyLease) CheckCurrent() error {
	if l == nil || l.manager == nil {
		return ErrVaultLocked
	}
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed || !l.manager.leaseCurrent(l) {
		return ErrVaultLocked
	}
	return nil
}

func (l *KeyLease) UseKey(work func([]byte) error) error {
	if l == nil || work == nil || l.manager == nil {
		return ErrVaultLocked
	}
	if !l.manager.leaseCurrent(l) {
		return ErrVaultLocked
	}
	l.mu.Lock()
	if l.closed || len(l.key) != VaultKeySize {
		l.mu.Unlock()
		return ErrVaultLocked
	}
	key := append([]byte(nil), l.key...)
	l.mu.Unlock()
	defer clear(key)
	return work(key)
}

// Commit publishes buffered output only while this lease still matches the
// current session grant and lock generation. The callback runs under a publication
// barrier, without the manager state mutex, and must not acquire repository
// locks or call back into KeyManager.
func (l *KeyLease) Commit(publish func() error) error {
	if l == nil || publish == nil || l.manager == nil {
		return ErrVaultLocked
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrVaultLocked
	}
	l.closed = true
	clear(l.key)
	l.key = nil
	l.mu.Unlock()

	return l.manager.guardLease(l, publish)
}

// GuardCommit validates a reusable lease at the durable mutation boundary.
// The callback must be a leaf transaction: acquire repository locks before
// calling this method and never acquire them (or call KeyManager) inside it.
func (l *KeyLease) GuardCommit(commit func() error) error {
	if l == nil || l.manager == nil || commit == nil {
		return ErrVaultLocked
	}
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return ErrVaultLocked
	}
	return l.manager.guardLease(l, commit)
}

func (m *KeyManager) guardLease(l *KeyLease, work func() error) error {
	m.commitGate.RLock()
	defer m.commitGate.RUnlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrKeyManagerClosed
	}
	state := m.vaults[l.vaultID]
	if !hasGrant(state, l.session) || state.generation != l.generation {
		m.mu.Unlock()
		return ErrVaultLocked
	}
	m.mu.Unlock()
	return work()
}

func (l *KeyLease) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	clear(l.key)
	l.key = nil
	l.closed = true
	l.mu.Unlock()
}

// LockVault clears the in-memory key and revokes every session grant for one
// vault. Subscribers receive a lock event even when the vault was already
// locked, allowing tabs to discard stale content.
func (m *KeyManager) LockVault(vaultID string, reason LockReason) error {
	if !validVaultID(vaultID) {
		return ErrInvalidVaultID
	}
	if reason == "" {
		reason = LockReasonManual
	}
	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrKeyManagerClosed
	}
	state := m.stateLocked(vaultID)
	m.lockStateLocked(vaultID, state, reason)
	m.scheduleIdleTimerLocked()
	return nil
}

// LockAll clears all keys and grants. Logout and server shutdown use this
// operation so a session transition cannot leave another tab with plaintext.
func (m *KeyManager) LockAll(reason LockReason) error {
	if reason == "" {
		reason = LockReasonManual
	}
	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrKeyManagerClosed
	}
	for vaultID, state := range m.vaults {
		m.lockStateLocked(vaultID, state, reason)
	}
	m.scheduleIdleTimerLocked()
	return nil
}

// RevokeSession removes one session's grants without disturbing other active
// sessions. Any lease held by that session fails its next Commit.
func (m *KeyManager) RevokeSession(vaultID string, sessionID []byte) error {
	if !validVaultID(vaultID) {
		return ErrInvalidVaultID
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return ErrSessionRequired
	}
	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrKeyManagerClosed
	}
	m.expireWithCommitGateLocked(time.Now())
	state := m.vaults[vaultID]
	if state == nil {
		return nil
	}
	if _, exists := state.grants[session]; exists {
		delete(state.grants, session)
		m.publishLocked(state, LockEvent{VaultID: vaultID, Locked: true, Generation: state.generation, Reason: LockReasonRevoked}, &session)
	}
	if len(state.grants) == 0 && len(state.key) != 0 {
		m.lockStateLocked(vaultID, state, LockReasonRevoked)
	}
	m.scheduleIdleTimerLocked()
	return nil
}

// RevokeSessionsExcept removes every grant except the current session. It is
// suitable for authentication operations that invalidate other sessions.
func (m *KeyManager) RevokeSessionsExcept(sessionID []byte) error {
	keep, ok := sessionKey(sessionID)
	if !ok {
		return ErrSessionRequired
	}
	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrKeyManagerClosed
	}
	m.expireWithCommitGateLocked(time.Now())
	for vaultID, state := range m.vaults {
		for session := range state.grants {
			if session == keep {
				continue
			}
			delete(state.grants, session)
			m.publishLocked(state, LockEvent{VaultID: vaultID, Locked: true, Generation: state.generation, Reason: LockReasonRevoked}, &session)
		}
		if len(state.grants) == 0 && len(state.key) != 0 {
			m.lockStateLocked(vaultID, state, LockReasonRevoked)
		}
	}
	m.scheduleIdleTimerLocked()
	return nil
}

// RevokeSessionEverywhere removes one authenticated session from all vaults.
func (m *KeyManager) RevokeSessionEverywhere(sessionID []byte) error {
	session, ok := sessionKey(sessionID)
	if !ok {
		return ErrSessionRequired
	}
	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrKeyManagerClosed
	}
	m.expireWithCommitGateLocked(time.Now())
	for vaultID, state := range m.vaults {
		if _, exists := state.grants[session]; !exists {
			continue
		}
		delete(state.grants, session)
		m.publishLocked(state, LockEvent{VaultID: vaultID, Locked: true, Generation: state.generation, Reason: LockReasonRevoked}, &session)
		if len(state.grants) == 0 && len(state.key) != 0 {
			m.lockStateLocked(vaultID, state, LockReasonRevoked)
		}
	}
	m.scheduleIdleTimerLocked()
	return nil
}

// Subscribe returns lock events scoped to one authenticated session. Every
// subscriber receives the current session-visible state immediately.
func (m *KeyManager) Subscribe(vaultID string, sessionID []byte) (<-chan LockEvent, func(), error) {
	if !validVaultID(vaultID) {
		return nil, nil, ErrInvalidVaultID
	}
	session, ok := sessionKey(sessionID)
	if !ok {
		return nil, nil, ErrSessionRequired
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, nil, ErrKeyManagerClosed
	}
	m.expireLocked(time.Now())
	m.scheduleIdleTimerLocked()
	state := m.stateLocked(vaultID)
	m.nextSubID++
	subID := m.nextSubID
	channel := make(chan LockEvent, 1)
	state.subscribers[subID] = keySubscriber{session: session, channel: channel}
	event := LockEvent{VaultID: vaultID, Locked: !hasGrant(state, session), Generation: state.generation, Reason: LockReasonInitial}
	channel <- event
	m.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if state.subscribers[subID].channel == channel {
				delete(state.subscribers, subID)
				close(channel)
			}
		})
	}
	return channel, cancel, nil
}

// Close revokes all in-memory grants, notifies subscribers, and prevents future
// unlocks. It is safe to call more than once.
func (m *KeyManager) Close() error {
	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	for vaultID, state := range m.vaults {
		m.lockStateLocked(vaultID, state, LockReasonRestart)
		for id, subscriber := range state.subscribers {
			delete(state.subscribers, id)
			close(subscriber.channel)
		}
	}
	m.closed = true
	return nil
}

func (m *KeyManager) stateLocked(vaultID string) *vaultKeyState {
	if m.vaults == nil {
		m.vaults = make(map[string]*vaultKeyState)
	}
	state := m.vaults[vaultID]
	if state == nil {
		state = &vaultKeyState{grants: make(map[[32]byte]keyGrant), subscribers: make(map[uint64]keySubscriber)}
		m.vaults[vaultID] = state
	}
	return state
}

func (m *KeyManager) lockStateLocked(vaultID string, state *vaultKeyState, reason LockReason) {
	clear(state.key)
	state.key = nil
	state.grants = make(map[[32]byte]keyGrant)
	state.lastActivity = time.Time{}
	state.expiresAt = time.Time{}
	state.generation++
	m.publishLocked(state, LockEvent{VaultID: vaultID, Locked: true, Generation: state.generation, Reason: reason}, nil)
}

func (m *KeyManager) publishLocked(state *vaultKeyState, event LockEvent, target *[32]byte) {
	for _, subscriber := range state.subscribers {
		if target != nil && subscriber.session != *target {
			continue
		}
		select {
		case subscriber.channel <- event:
		default:
			select {
			case <-subscriber.channel:
			default:
			}
			select {
			case subscriber.channel <- event:
			default:
			}
		}
	}
}

// Ordinary state inspection must not wait for publication while holding mu.
func (m *KeyManager) expireLocked(now time.Time) {
	if !m.commitGate.TryLock() {
		return
	}
	defer m.commitGate.Unlock()
	m.expireWithCommitGateLocked(now)
}

func (m *KeyManager) expireWithCommitGateLocked(now time.Time) {
	for vaultID, state := range m.vaults {
		if len(state.key) == VaultKeySize && !state.lastActivity.IsZero() && !now.Before(state.lastActivity.Add(m.timeout)) {
			m.lockStateLocked(vaultID, state, LockReasonIdle)
		}
	}
}

func (m *KeyManager) scheduleIdleTimerLocked() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	if m.closed {
		return
	}
	var deadline time.Time
	for _, state := range m.vaults {
		if len(state.key) != VaultKeySize || state.lastActivity.IsZero() {
			continue
		}
		candidate := state.lastActivity.Add(m.timeout)
		if deadline.IsZero() || candidate.Before(deadline) {
			deadline = candidate
		}
	}
	if deadline.IsZero() {
		return
	}
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	m.timer = time.AfterFunc(delay, m.idleTimerFired)
}

func (m *KeyManager) idleTimerFired() {
	m.commitGate.Lock()
	defer m.commitGate.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.expireWithCommitGateLocked(time.Now())
	m.scheduleIdleTimerLocked()
}

func hasGrant(state *vaultKeyState, session [32]byte) bool {
	if state == nil || len(state.key) != VaultKeySize || (!state.expiresAt.IsZero() && !time.Now().Before(state.expiresAt)) {
		return false
	}
	grant, ok := state.grants[session]
	return ok && grant.generation == state.generation
}

func (m *KeyManager) leaseCurrent(lease *KeyLease) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked(time.Now())
	if m.closed {
		return false
	}
	state := m.vaults[lease.vaultID]
	if !hasGrant(state, lease.session) || state.generation != lease.generation {
		return false
	}
	return true
}

func sessionKey(sessionID []byte) ([32]byte, bool) {
	var result [32]byte
	if len(sessionID) != len(result) {
		return result, false
	}
	copy(result[:], sessionID)
	return result, true
}

func validVaultID(vaultID string) bool {
	return len(vaultID) > 0 && len(vaultID) <= 128 && utf8.ValidString(vaultID) && !containsControl(vaultID)
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
