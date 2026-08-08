package action

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AuditRecord is the full trail of an applied action, appended to the
// incident record as a JSONL line: user, timestamp, cluster, resource,
// action, arguments, evidence_ids, reason, risk, approval, result.
type AuditRecord struct {
	Kind        string            `json:"kind"` // "action.audit"
	User        string            `json:"user"`
	Timestamp   string            `json:"timestamp"`
	ClusterID   string            `json:"cluster_id"`
	IncidentID  string            `json:"incident_id"`
	Action      string            `json:"action"`
	Resource    string            `json:"resource"`
	Arguments   map[string]string `json:"arguments,omitempty"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
	Reason      string            `json:"reason"`
	Risk        string            `json:"risk"`
	Approval    string            `json:"approval"` // "explicit" | "preview-only"
	Result      string            `json:"result"`
	Error       string            `json:"error,omitempty"`
}

// NewAudit builds an audit record for an applied action.
func NewAudit(user, clusterID, incidentID string, act Action, approval, result, errMsg string) AuditRecord {
	return AuditRecord{
		Kind:        "action.audit",
		User:        user,
		Timestamp:   time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		ClusterID:   clusterID,
		IncidentID:  incidentID,
		Action:      string(act.Type),
		Resource:    act.Target.String(),
		Arguments:   act.Args,
		EvidenceIDs: act.EvidenceIDs,
		Reason:      act.Reason,
		Risk:        string(act.Risk),
		Approval:    approval,
		Result:      result,
		Error:       errMsg,
	}
}

// AuditSink appends audit records to the incident file. Stored as a separate
// interface so the action package does not depend on the record package.
type AuditSink interface {
	AppendAudit(incidentID string, rec AuditRecord) error
}

// FileAuditSink appends audit lines to <dir>/<incident-id>.jsonl.
type FileAuditSink struct{ Dir string }

func (s FileAuditSink) AppendAudit(incidentID string, rec AuditRecord) error {
	path := incidentID
	if !strings.HasSuffix(path, ".jsonl") {
		path += ".jsonl"
	}
	path = filepath.Join(s.Dir, path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(rec)
}
