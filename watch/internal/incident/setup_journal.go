package incident

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	// SetupJournalFile is the journal's name inside the worker's state directory.
	SetupJournalFile = "setup-journal.json"

	setupJournalVersion = 1
	maxJournalBytes     = 1 << 20
)

// Setup step statuses. A step is pending until it has been attempted.
const (
	StepPending   = "pending"
	StepDone      = "done"      // this run changed something
	StepUnchanged = "unchanged" // the wanted state already held
	StepFailed    = "failed"
)

// SetupStep is the progress of one apply step. Result holds a short failure
// class or note, never az output.
type SetupStep struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Result    string    `json:"result,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SetupJournal is the non-secret record of one `cenci incident setup --apply`
// request. It makes a rerun resumable and refuses a rerun for a different
// request. Azure stays the source of truth: the journal records progress,
// binding and completion, not what exists.
type SetupJournal struct {
	Version     int         `json:"version"`
	Tenant      string      `json:"tenant"`
	Identity    string      `json:"identity"`
	ConfigPath  string      `json:"configPath"` // absolute
	PrincipalID string      `json:"principalId,omitempty"`
	ClientID    string      `json:"clientId,omitempty"`
	Steps       []SetupStep `json:"steps"`
	Complete    bool        `json:"complete"`
	CreatedAt   time.Time   `json:"createdAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`

	// LastRoleCreateAt is when the most recent role-assignment create was
	// attempted. It is written before each attempt, so a create that failed or
	// was interrupted still leaves it, and it is kept across runs: a rerun finds
	// every assignment present yet cannot tell whether they have propagated.
	// Nil when no create was ever attempted (and in journals written before it
	// existed).
	LastRoleCreateAt *time.Time `json:"lastRoleCreateAt,omitempty"`
}

// NewSetupJournal starts a journal bound to the request, with no steps.
func NewSetupJournal(tenant, identity, configPath string, now time.Time) *SetupJournal {
	return &SetupJournal{Version: setupJournalVersion, Tenant: tenant, Identity: identity, ConfigPath: configPath, Steps: []SetupStep{}, CreatedAt: now, UpdatedAt: now}
}

// SetupJournalPath is where the journal lives for a state directory.
func SetupJournalPath(stateDir string) string { return filepath.Join(stateDir, SetupJournalFile) }

// CheckBinding fails unless the journal was written for this tenant (compared
// without case), identity mode and exact absolute config path.
func (j *SetupJournal) CheckBinding(tenant, identity, configPath string) error {
	var field string
	switch {
	case !strings.EqualFold(j.Tenant, tenant):
		field = "tenant"
	case j.Identity != identity:
		field = "identity"
	case j.ConfigPath != configPath:
		field = "config path"
	default:
		return nil
	}
	return fmt.Errorf("the setup journal %s belongs to a different request (%s differs); it was left untouched", SetupJournalFile, field)
}

// SetStep records the status of a step, adding it at the end when new.
func (j *SetupJournal) SetStep(name, status, result string, now time.Time) {
	i := slices.IndexFunc(j.Steps, func(s SetupStep) bool { return s.Name == name })
	if i < 0 {
		j.Steps = append(j.Steps, SetupStep{Name: name})
		i = len(j.Steps) - 1
	}
	j.Steps[i].Status, j.Steps[i].Result, j.Steps[i].UpdatedAt = status, result, now
	j.UpdatedAt = now
}

// readSetupJournal reads the journal of a state directory, returning (nil, nil)
// when there is none. A symbolic link, a file that is not a regular file owned
// by uid, an oversized or malformed file is an error: callers must stop and
// leave such a file alone.
func readSetupJournal(stateDir string, uid int) (*SetupJournal, error) {
	path := SetupJournalPath(stateDir)
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil, nil
		case errors.Is(err, syscall.ELOOP):
			return nil, fmt.Errorf("setup journal %s is a symbolic link; left untouched", path)
		}
		return nil, fmt.Errorf("setup journal %s unreadable: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("setup journal %s unreadable: %w", path, err)
	}
	if why := notOwnedRegular(fi, uid); why != "" {
		return nil, fmt.Errorf("setup journal %s %s; left untouched", path, why)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxJournalBytes+1))
	if err != nil {
		return nil, fmt.Errorf("setup journal %s unreadable: %w", path, err)
	}
	if len(data) > maxJournalBytes {
		return nil, fmt.Errorf("setup journal %s is too large (limit %d bytes); left untouched", path, maxJournalBytes)
	}
	j, err := decodeSetupJournal(data)
	if err != nil {
		return nil, fmt.Errorf("setup journal %s is corrupt (%v); left untouched", path, err)
	}
	return j, nil
}

func decodeSetupJournal(data []byte) (*SetupJournal, error) {
	var j SetupJournal
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("extra value")
	}
	if j.Version != setupJournalVersion || !filepath.IsAbs(j.ConfigPath) || j.Tenant == "" || j.Identity == "" {
		return nil, errors.New("unsupported version or missing binding")
	}
	for _, s := range j.Steps {
		switch s.Status {
		case StepPending, StepDone, StepUnchanged, StepFailed:
		default:
			return nil, errors.New("unknown step status")
		}
	}
	return &j, nil
}

// Write persists the journal atomically with mode 0600, creating the state
// directory 0700 first like the incident ledger does.
func (j *SetupJournal) Write(stateDir string) error {
	if !filepath.IsAbs(stateDir) {
		return fmt.Errorf("state directory must be absolute")
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return durableFile(SetupJournalPath(stateDir), append(b, '\n'))
}
