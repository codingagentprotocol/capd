package server

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const maxAttachments = 16

// policyEngine rejects malformed or unusually risky CAP input before
// subprocess-backed adapters see it.
type policyEngine struct{}

func newPolicyEngine() *policyEngine { return &policyEngine{} }

func (e *policyEngine) validateSessionCreate(params protocol.SessionCreateParams) *protocol.Error {
	if params.AgentID == "" {
		return protocol.NewError(protocol.CodeInvalidParams, "agentId is required")
	}
	if !validPermissionMode(params.PermissionMode) {
		return protocol.NewError(protocol.CodeInvalidParams, "unknown permissionMode %q", params.PermissionMode)
	}
	if params.PermissionMode == protocol.PermissionFull && isFilesystemRoot(params.Cwd) {
		return protocol.NewError(protocol.CodeInvalidParams, "permissionMode %q is not allowed at filesystem root", protocol.PermissionFull)
	}
	return nil
}

func (e *policyEngine) validateTaskSend(params protocol.TaskSendParams) *protocol.Error {
	if params.SessionID == "" {
		return protocol.NewError(protocol.CodeInvalidParams, "sessionId is required")
	}
	if strings.TrimSpace(params.Prompt) == "" && len(params.Attachments) == 0 {
		return protocol.NewError(protocol.CodeInvalidParams, "prompt or attachments are required")
	}
	if len(params.Attachments) > maxAttachments {
		return protocol.NewError(protocol.CodeInvalidParams, "too many attachments: max %d", maxAttachments)
	}
	for i, att := range params.Attachments {
		if perr := validateAttachment(i, att); perr != nil {
			return perr
		}
	}
	return nil
}

func (e *policyEngine) validateApprovalReply(params protocol.ApprovalReplyParams) *protocol.Error {
	if params.SessionID == "" {
		return protocol.NewError(protocol.CodeInvalidParams, "sessionId is required")
	}
	if params.ApprovalID == "" {
		return protocol.NewError(protocol.CodeInvalidParams, "approvalId is required")
	}
	switch params.Decision {
	case protocol.DecisionApprove, protocol.DecisionApproveAlways, protocol.DecisionDeny:
		return nil
	default:
		return protocol.NewError(protocol.CodeInvalidParams, "unknown decision %q", params.Decision)
	}
}

func validateAttachment(i int, att protocol.Attachment) *protocol.Error {
	if att.Type != "image" {
		return protocol.NewError(protocol.CodeInvalidParams, "attachments[%d].type must be %q", i, "image")
	}
	hasPath := att.Path != ""
	hasURL := att.URL != ""
	if hasPath == hasURL {
		return protocol.NewError(protocol.CodeInvalidParams, "attachments[%d] must set exactly one of path or url", i)
	}
	if hasPath {
		if !filepath.IsAbs(att.Path) {
			return protocol.NewError(protocol.CodeInvalidParams, "attachments[%d].path must be absolute", i)
		}
		return nil
	}
	u, err := url.Parse(att.URL)
	if err != nil || u.Host == "" {
		return protocol.NewError(protocol.CodeInvalidParams, "attachments[%d].url must be absolute", i)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return protocol.NewError(protocol.CodeInvalidParams, "attachments[%d].url must use http or https", i)
	}
	return nil
}

func validPermissionMode(mode string) bool {
	switch mode {
	case protocol.PermissionDefault, protocol.PermissionAcceptEdits, protocol.PermissionFull:
		return true
	default:
		return false
	}
}

func isFilesystemRoot(path string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = filepath.VolumeName(abs) + string(filepath.Separator)
	}
	return filepath.Clean(abs) == filepath.Clean(root)
}
