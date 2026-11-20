package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"src.solsynth.dev/sosys/filesystem/database"
	gen "src.solsynth.dev/sosys/lighthouse/shared/proto"
)

// PermissionMask is a bitmask of file/folder capabilities.
type PermissionMask uint32

const (
	PermNone   PermissionMask = 0
	PermRead   PermissionMask = 1 << 0 // View metadata, download content, list folder children
	PermWrite  PermissionMask = 1 << 1 // Create/upload children, overwrite content, rename/patch
	PermDelete PermissionMask = 1 << 2 // Recycle, restore, delete, purge, move out
	PermShare  PermissionMask = 1 << 3 // Grant/revoke access up to own permissions
	PermManage PermissionMask = 1 << 4 // Full ACL management, toggle inherit_acl

	RoleViewer      PermissionMask = PermRead
	RoleEditor      PermissionMask = PermRead | PermWrite
	RoleContributor PermissionMask = PermRead | PermWrite | PermDelete
	RoleManager     PermissionMask = PermRead | PermWrite | PermDelete | PermShare | PermManage
	PermAll         PermissionMask = RoleManager
)

const (
	ACLEffectAllow = "allow"
	ACLEffectDeny  = "deny"

	ACLSubjectAccount        = "account"
	ACLSubjectWorkspace      = "workspace"
	ACLSubjectWorkspaceRole  = "workspace_role"
	ACLSubjectWorkspaceGroup = "workspace_group"
	ACLSubjectAuthenticated  = "authenticated"
	ACLSubjectPublic         = "public"
	ACLSubjectLink           = "link"
	ACLSubjectScope          = "scope"
)

// Workspace member role levels matching Valve's WorkspaceMemberRole constants.
const (
	WorkspaceRoleViewer int32 = 25
	WorkspaceRoleMember int32 = 50
	WorkspaceRoleAdmin  int32 = 75
	WorkspaceRoleOwner  int32 = 100
)

// WorkspaceACLResolver resolves a caller's workspace membership role and
// workspace-scoped account group IDs. Implemented by QuotaService.
type WorkspaceACLResolver interface {
	GetWorkspaceMemberRole(ctx context.Context, workspaceID, accountID string) (int32, bool)
	GetAccountWorkspaceGroups(ctx context.Context, workspaceID, accountID string) ([]string, error)
}

// SetWorkspaceACLResolver wires workspace role and group resolution into the
// ACL engine.
func (s *FileService) SetWorkspaceACLResolver(r WorkspaceACLResolver) {
	s.workspaceACL = r
}

// ParsePermissionMask converts a role name, legacy permission string, or
// comma-separated capability list into a PermissionMask.
func ParsePermissionMask(name string) (PermissionMask, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "viewer":
		return RoleViewer, nil
	case "write", "editor":
		return RoleEditor, nil
	case "delete", "contributor":
		return RoleContributor, nil
	case "share":
		return PermRead | PermShare, nil
	case "manage", "manager", "owner", "admin":
		return RoleManager, nil
	default:
		return PermNone, fmt.Errorf("unknown permission or role %q", name)
	}
}

// MaskToRole returns the canonical role preset name if mask matches one, or
// "custom" otherwise.
func MaskToRole(mask PermissionMask) string {
	switch mask & PermAll {
	case PermNone:
		return "none"
	case RoleViewer:
		return "viewer"
	case RoleEditor:
		return "editor"
	case RoleContributor:
		return "contributor"
	case RoleManager:
		return "manager"
	default:
		return "custom"
	}
}

// WorkspaceRoleName maps Valve's numeric workspace role level to its ACL name.
func WorkspaceRoleName(level int32) string {
	switch {
	case level >= WorkspaceRoleOwner:
		return "owner"
	case level >= WorkspaceRoleAdmin:
		return "admin"
	case level >= WorkspaceRoleMember:
		return "member"
	case level >= WorkspaceRoleViewer:
		return "viewer"
	default:
		return ""
	}
}

// WorkspaceRoleBaselineMask returns the default file permission mask granted to
// an active workspace member when ACL inheritance has not been broken.
func WorkspaceRoleBaselineMask(level int32) PermissionMask {
	switch {
	case level >= WorkspaceRoleAdmin:
		return RoleManager
	case level >= WorkspaceRoleMember:
		return RoleEditor
	case level >= WorkspaceRoleViewer:
		return RoleViewer
	default:
		return PermNone
	}
}

type wsRoleMemo struct {
	role int32
	ok   bool
}

// PrincipalSet represents the authenticated (or anonymous) caller for a single
// request and caches lazy workspace role/group lookups for its lifetime.
type PrincipalSet struct {
	AccountID       string
	IsSuperuser     bool
	Authenticated   bool
	Scopes          map[string]struct{}
	ShareLinkID     string
	workspaceRoles  map[string]wsRoleMemo
	workspaceGroups map[string]map[string]struct{}
}

// NewPrincipalSet builds a PrincipalSet from an optional DyAccount and session.
func NewPrincipalSet(account *gen.DyAccount, session *gen.DyAuthSession) *PrincipalSet {
	p := &PrincipalSet{}
	if account != nil && strings.TrimSpace(account.GetId()) != "" {
		p.AccountID = strings.TrimSpace(account.GetId())
		p.IsSuperuser = account.GetIsSuperuser()
		p.Authenticated = true
	}
	if session != nil && len(session.GetScopes()) > 0 {
		p.Scopes = make(map[string]struct{}, len(session.GetScopes()))
		for _, scope := range session.GetScopes() {
			if s := strings.TrimSpace(scope); s != "" {
				p.Scopes[s] = struct{}{}
			}
		}
	}
	return p
}

func (p *PrincipalSet) workspaceRole(ctx context.Context, resolver WorkspaceACLResolver, workspaceID string) (int32, bool) {
	if p == nil || !p.Authenticated || resolver == nil {
		return 0, false
	}
	wsID := strings.TrimSpace(workspaceID)
	if wsID == "" {
		return 0, false
	}
	if p.workspaceRoles == nil {
		p.workspaceRoles = make(map[string]wsRoleMemo)
	}
	if memo, ok := p.workspaceRoles[wsID]; ok {
		return memo.role, memo.ok
	}
	role, ok := resolver.GetWorkspaceMemberRole(ctx, wsID, p.AccountID)
	p.workspaceRoles[wsID] = wsRoleMemo{role: role, ok: ok}
	return role, ok
}

func (p *PrincipalSet) hasWorkspaceGroup(ctx context.Context, resolver WorkspaceACLResolver, workspaceID, groupID string) bool {
	if p == nil || !p.Authenticated || resolver == nil {
		return false
	}
	wsID := strings.TrimSpace(workspaceID)
	gid := strings.TrimSpace(groupID)
	if wsID == "" || gid == "" {
		return false
	}
	if p.workspaceGroups == nil {
		p.workspaceGroups = make(map[string]map[string]struct{})
	}
	groups, loaded := p.workspaceGroups[wsID]
	if !loaded {
		ids, err := resolver.GetAccountWorkspaceGroups(ctx, wsID, p.AccountID)
		groups = make(map[string]struct{}, len(ids))
		if err == nil {
			for _, id := range ids {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					groups[trimmed] = struct{}{}
				}
			}
		}
		p.workspaceGroups[wsID] = groups
	}
	_, ok := groups[gid]
	return ok
}

func (p *PrincipalSet) matchesEntry(ctx context.Context, resolver WorkspaceACLResolver, fileWorkspaceID *string, entry database.FileACLEntry) bool {
	switch entry.SubjectType {
	case ACLSubjectPublic:
		return true
	case ACLSubjectAuthenticated:
		return p != nil && p.Authenticated
	case ACLSubjectAccount:
		return p != nil && p.Authenticated && entry.SubjectID == p.AccountID
	case ACLSubjectScope:
		if p == nil || len(p.Scopes) == 0 {
			return false
		}
		_, ok := p.Scopes[entry.SubjectID]
		return ok
	case ACLSubjectLink:
		return p != nil && p.ShareLinkID != "" && entry.SubjectID == p.ShareLinkID
	case ACLSubjectWorkspace:
		if p == nil || !p.Authenticated || entry.SubjectID == "" {
			return false
		}
		_, ok := p.workspaceRole(ctx, resolver, entry.SubjectID)
		return ok
	case ACLSubjectWorkspaceRole:
		if p == nil || !p.Authenticated || entry.SubjectID == "" {
			return false
		}
		wsID, roleName, found := strings.Cut(entry.SubjectID, ":")
		if !found || wsID == "" || roleName == "" {
			return false
		}
		role, ok := p.workspaceRole(ctx, resolver, wsID)
		if !ok {
			return false
		}
		return strings.EqualFold(WorkspaceRoleName(role), roleName)
	case ACLSubjectWorkspaceGroup:
		// Account groups only apply to workspace files, never individual files.
		if p == nil || !p.Authenticated || fileWorkspaceID == nil || strings.TrimSpace(*fileWorkspaceID) == "" {
			return false
		}
		wsID := strings.TrimSpace(*fileWorkspaceID)
		groupID := entry.SubjectID
		if prefix, rest, found := strings.Cut(entry.SubjectID, ":"); found && prefix != "" && rest != "" {
			wsID = prefix
			groupID = rest
		}
		return p.hasWorkspaceGroup(ctx, resolver, wsID, groupID)
	default:
		return false
	}
}

// ACLEvaluation is the resolved effective permission summary for one file.
type ACLEvaluation struct {
	Mask          PermissionMask
	Visibility    string
	InheritedFrom *string
	InheritACL    bool
}

func (e ACLEvaluation) ToPermissionStatus() database.PermissionStatus {
	return database.PermissionStatus{
		Readable:      e.Mask&PermRead != 0,
		Writable:      e.Mask&PermWrite != 0,
		Deletable:     e.Mask&PermDelete != 0,
		Shareable:     e.Mask&PermShare != 0,
		Manageable:    e.Mask&PermManage != 0,
		Visibility:    e.Visibility,
		InheritACL:    e.InheritACL,
		EffectiveMask: uint32(e.Mask),
		InheritedFrom: e.InheritedFrom,
	}
}

type ancestorMeta struct {
	ID         string
	InheritACL bool
	HasACL     bool
}

// EvaluateFiles computes effective ACL permissions for a batch of files in
// O(1) flat SQL queries (zero recursive CTEs).
func (s *FileService) EvaluateFiles(ctx context.Context, caller *PrincipalSet, files []database.CloudFile) (map[string]ACLEvaluation, error) {
	out := make(map[string]ACLEvaluation, len(files))
	if len(files) == 0 {
		return out, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Resolve source files for any derivatives in one batch so derivatives
	// inherit their source file's exact ACL.
	sourceIDs := make([]string, 0)
	derivToSource := make(map[string]string)
	for i := range files {
		if files[i].DerivedFromID != nil && strings.TrimSpace(*files[i].DerivedFromID) != "" {
			srcID := strings.TrimSpace(*files[i].DerivedFromID)
			derivToSource[files[i].ID] = srcID
			sourceIDs = append(sourceIDs, srcID)
		}
	}
	sourcesByID := make(map[string]database.CloudFile, len(sourceIDs))
	if len(sourceIDs) > 0 {
		var srcs []database.CloudFile
		if err := s.db.WithContext(ctx).
			Select("id", "account_id", "workspace_id", "pool_id", "parent_id", "path", "depth", "inherit_acl", "has_acl").
			Where("id IN ? AND deleted_at IS NULL", normalizeFileIDs(sourceIDs)).
			Find(&srcs).Error; err != nil {
			return nil, err
		}
		for i := range srcs {
			sourcesByID[srcs[i].ID] = srcs[i]
		}
	}

	// Effective targets to evaluate (source file for derivatives, self otherwise).
	targets := make([]database.CloudFile, len(files))
	for i := range files {
		if srcID, ok := derivToSource[files[i].ID]; ok {
			if src, found := sourcesByID[srcID]; found {
				targets[i] = src
				continue
			}
		}
		targets[i] = files[i]
	}

	// Collect all unique ancestor IDs from materialized paths (zero DB queries).
	ancestorIDSet := make(map[string]struct{})
	for i := range targets {
		for _, ancID := range AncestorIDsFromPath(targets[i].Path) {
			ancestorIDSet[ancID] = struct{}{}
		}
	}

	ancestorsByID := make(map[string]ancestorMeta, len(ancestorIDSet))
	if len(ancestorIDSet) > 0 {
		ancIDs := make([]string, 0, len(ancestorIDSet))
		for id := range ancestorIDSet {
			ancIDs = append(ancIDs, id)
		}
		var rows []ancestorMeta
		if err := s.db.WithContext(ctx).Model(&database.CloudFile{}).
			Select("id, inherit_acl, has_acl").
			Where("id IN ? AND deleted_at IS NULL", ancIDs).
			Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			ancestorsByID[r.ID] = r
		}
	}

	// Determine the effective chain per target (stopping at inherit_acl == false)
	// and collect only node IDs where has_acl == true.
	type fileChain struct {
		nodeIDs           []string // ordered self -> parent -> ... -> boundary
		inheritanceBroken bool
	}
	chains := make([]fileChain, len(targets))
	aclNodeIDSet := make(map[string]struct{})

	for i := range targets {
		f := &targets[i]
		chain := fileChain{nodeIDs: []string{f.ID}}
		if f.HasACL {
			aclNodeIDSet[f.ID] = struct{}{}
		}
		if !f.InheritACL {
			chain.inheritanceBroken = true
			chains[i] = chain
			continue
		}
		ancs := AncestorIDsFromPath(f.Path)
		// Walk nearest parent up to root.
		for j := len(ancs) - 1; j >= 0; j-- {
			ancID := ancs[j]
			meta, ok := ancestorsByID[ancID]
			if !ok {
				continue
			}
			chain.nodeIDs = append(chain.nodeIDs, ancID)
			if meta.HasACL {
				aclNodeIDSet[ancID] = struct{}{}
			}
			if !meta.InheritACL {
				chain.inheritanceBroken = true
				break
			}
		}
		chains[i] = chain
	}

	// Single flat indexed query for all active ACL entries on nodes with has_acl = true.
	entriesByFile := make(map[string][]database.FileACLEntry, len(aclNodeIDSet))
	if len(aclNodeIDSet) > 0 {
		aclNodeIDs := make([]string, 0, len(aclNodeIDSet))
		for id := range aclNodeIDSet {
			aclNodeIDs = append(aclNodeIDs, id)
		}
		now := time.Now().UTC()
		var entries []database.FileACLEntry
		if err := s.db.WithContext(ctx).
			Where("file_id IN ? AND (expires_at IS NULL OR expires_at > ?)", aclNodeIDs, now).
			Find(&entries).Error; err != nil {
			return nil, err
		}
		for _, entry := range entries {
			entriesByFile[entry.FileID] = append(entriesByFile[entry.FileID], entry)
		}
	}

	// Shared-pool default (account-less pools stay public-readable unless denied).
	sharedPools, err := s.sharedPoolFiles(targets)
	if err != nil {
		return nil, err
	}

	for i := range files {
		orig := &files[i]
		f := &targets[i]
		chain := chains[i]

		var allowMask, denyMask PermissionMask
		publicAllow := false
		publicDeny := false
		hasExplicitShare := false
		var inheritedFrom *string

		for idx, nodeID := range chain.nodeIDs {
			isAncestor := idx > 0
			for _, entry := range entriesByFile[nodeID] {
				if isAncestor && !entry.Inheritable {
					continue
				}
				if isAncestor && inheritedFrom == nil {
					idCopy := nodeID
					inheritedFrom = &idCopy
				}
				if entry.SubjectType == ACLSubjectPublic {
					// Anonymous visibility is computed from publicAllow/publicDeny
					// below; a public deny retracts the shared-pool default but
					// must not cancel an account or workspace grant.
					if entry.Effect == ACLEffectDeny && PermissionMask(entry.Permissions)&PermRead != 0 {
						publicDeny = true
					} else if entry.Effect == ACLEffectAllow && PermissionMask(entry.Permissions)&PermRead != 0 {
						publicAllow = true
					}
					continue
				}
				if entry.Effect == ACLEffectAllow {
					hasExplicitShare = true
				}
				if caller != nil && caller.matchesEntry(ctx, s.workspaceACL, f.WorkspaceID, entry) {
					if entry.Effect == ACLEffectDeny {
						denyMask |= PermissionMask(entry.Permissions)
					} else {
						allowMask |= PermissionMask(entry.Permissions)
					}
				}
			}
		}

		// Workspace baseline when inheritance is intact.
		if f.WorkspaceID != nil && strings.TrimSpace(*f.WorkspaceID) != "" && !chain.inheritanceBroken && caller != nil {
			if role, ok := caller.workspaceRole(ctx, s.workspaceACL, *f.WorkspaceID); ok {
				allowMask |= WorkspaceRoleBaselineMask(role)
			}
		}

		// A public allow publishes read for everyone, signed in or not.
		if publicAllow && !publicDeny {
			allowMask |= PermRead
		}

		// Shared-pool public read fallback (preserved for social/chat links).
		isSharedPool := sharedPools[f.ID]
		if isSharedPool && !chain.inheritanceBroken && !publicDeny {
			publicAllow = true
			allowMask |= PermRead
		}

		effectiveMask := allowMask &^ denyMask

		// Superuser and personal file owners always retain full control.
		if caller != nil && caller.IsSuperuser {
			effectiveMask = PermAll
		} else if caller != nil && caller.Authenticated &&
			(f.WorkspaceID == nil || strings.TrimSpace(*f.WorkspaceID) == "") &&
			f.AccountID.String() == caller.AccountID {
			effectiveMask = PermAll
		}

		visibility := "private"
		if publicAllow && !publicDeny {
			visibility = "public"
		} else if hasExplicitShare {
			visibility = "restricted"
		}

		out[orig.ID] = ACLEvaluation{
			Mask:          effectiveMask,
			Visibility:    visibility,
			InheritedFrom: inheritedFrom,
			InheritACL:    f.InheritACL,
		}
	}

	return out, nil
}

// EvaluateFile evaluates a single file's effective ACL for caller.
func (s *FileService) EvaluateFile(ctx context.Context, caller *PrincipalSet, file *database.CloudFile) (ACLEvaluation, error) {
	if file == nil {
		return ACLEvaluation{Visibility: "private"}, nil
	}
	res, err := s.EvaluateFiles(ctx, caller, []database.CloudFile{*file})
	if err != nil {
		return ACLEvaluation{Visibility: "private"}, err
	}
	return res[file.ID], nil
}

// CanAccessFileMask reports whether caller holds every bit in required on file.
func (s *FileService) CanAccessFileMask(ctx context.Context, caller *PrincipalSet, file *database.CloudFile, required PermissionMask) bool {
	if file == nil {
		return false
	}
	eval, err := s.EvaluateFile(ctx, caller, file)
	if err != nil {
		return false
	}
	return eval.Mask&required == required
}

// PopulateFilesMetadataForCaller fills ChildrenCount (folders only) and
// caller-accurate PermissionStatus for a batch of files.
func (s *FileService) PopulateFilesMetadataForCaller(ctx context.Context, caller *PrincipalSet, files []database.CloudFile) error {
	if len(files) == 0 {
		return nil
	}
	folderIDs := make([]string, 0, len(files))
	for i := range files {
		if files[i].IsFolder && strings.TrimSpace(files[i].ID) != "" {
			folderIDs = append(folderIDs, files[i].ID)
		}
	}
	childCounts, err := s.countChildrenBatch(folderIDs)
	if err != nil {
		return err
	}
	evals, err := s.EvaluateFiles(ctx, caller, files)
	if err != nil {
		return err
	}
	for i := range files {
		if files[i].IsFolder {
			files[i].ChildrenCount = childCounts[files[i].ID]
		}
		if eval, ok := evals[files[i].ID]; ok {
			files[i].PermissionStatus = eval.ToPermissionStatus()
		}
	}
	return nil
}

// InheritedACLEntry pairs an inherited FileACLEntry with the ancestor folder it
// came from so UIs can show where a rule originated.
type InheritedACLEntry struct {
	database.FileACLEntry
	SourceFileID   string `json:"source_file_id"`
	SourceFileName string `json:"source_file_name"`
}

// FileACLDetail is the structured response for GET /api/files/:id/acl.
type FileACLDetail struct {
	FileID     string                    `json:"file_id"`
	InheritACL bool                      `json:"inherit_acl"`
	Direct     []database.FileACLEntry   `json:"direct"`
	Inherited  []InheritedACLEntry       `json:"inherited"`
	Effective  database.PermissionStatus `json:"effective"`
}

// GetFileACL returns the direct and inherited ACL entries plus the caller's
// effective permission status for file.
func (s *FileService) GetFileACL(ctx context.Context, caller *PrincipalSet, file *database.CloudFile) (*FileACLDetail, error) {
	if file == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	eval, err := s.EvaluateFile(ctx, caller, file)
	if err != nil {
		return nil, err
	}

	var direct []database.FileACLEntry
	if err := s.db.WithContext(ctx).
		Where("file_id = ?", file.ID).
		Order("created_at ASC, id ASC").
		Find(&direct).Error; err != nil {
		return nil, err
	}
	if direct == nil {
		direct = []database.FileACLEntry{}
	}

	inherited := []InheritedACLEntry{}
	if file.InheritACL {
		ancIDs := AncestorIDsFromPath(file.Path)
		if len(ancIDs) > 0 {
			var ancFiles []database.CloudFile
			if err := s.db.WithContext(ctx).
				Select("id", "name", "inherit_acl", "has_acl").
				Where("id IN ? AND deleted_at IS NULL", ancIDs).
				Find(&ancFiles).Error; err != nil {
				return nil, err
			}
			byID := make(map[string]database.CloudFile, len(ancFiles))
			for _, af := range ancFiles {
				byID[af.ID] = af
			}

			effectiveAncIDs := make([]string, 0, len(ancIDs))
			for j := len(ancIDs) - 1; j >= 0; j-- {
				af, ok := byID[ancIDs[j]]
				if !ok {
					continue
				}
				if af.HasACL {
					effectiveAncIDs = append(effectiveAncIDs, af.ID)
				}
				if !af.InheritACL {
					break
				}
			}
			if len(effectiveAncIDs) > 0 {
				var ancEntries []database.FileACLEntry
				if err := s.db.WithContext(ctx).
					Where("file_id IN ? AND inheritable = true", effectiveAncIDs).
					Order("created_at ASC, id ASC").
					Find(&ancEntries).Error; err != nil {
					return nil, err
				}
				byAnc := make(map[string][]database.FileACLEntry, len(effectiveAncIDs))
				for _, e := range ancEntries {
					byAnc[e.FileID] = append(byAnc[e.FileID], e)
				}
				for _, ancID := range effectiveAncIDs {
					af := byID[ancID]
					for _, e := range byAnc[ancID] {
						inherited = append(inherited, InheritedACLEntry{
							FileACLEntry:   e,
							SourceFileID:   af.ID,
							SourceFileName: af.Name,
						})
					}
				}
			}
		}
	}

	return &FileACLDetail{
		FileID:     file.ID,
		InheritACL: file.InheritACL,
		Direct:     direct,
		Inherited:  inherited,
		Effective:  eval.ToPermissionStatus(),
	}, nil
}

// ACLEntryInput is the write payload for creating/replacing ACL entries.
type ACLEntryInput struct {
	SubjectType string     `json:"subject_type"`
	SubjectID   string     `json:"subject_id"`
	Effect      string     `json:"effect"`
	Permissions uint32     `json:"permissions"`
	Role        string     `json:"role,omitempty"`
	Inheritable *bool      `json:"inheritable,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func normalizeACLEntryInput(file *database.CloudFile, in ACLEntryInput, actorID *uuid.UUID) (database.FileACLEntry, error) {
	st := strings.ToLower(strings.TrimSpace(in.SubjectType))
	sid := strings.TrimSpace(in.SubjectID)
	effect := strings.ToLower(strings.TrimSpace(in.Effect))
	if effect == "" {
		effect = ACLEffectAllow
	}
	if effect != ACLEffectAllow && effect != ACLEffectDeny {
		return database.FileACLEntry{}, fmt.Errorf("invalid effect %q (must be allow or deny)", in.Effect)
	}

	isWorkspaceFile := file != nil && file.WorkspaceID != nil && strings.TrimSpace(*file.WorkspaceID) != ""
	switch st {
	case ACLSubjectPublic, ACLSubjectAuthenticated:
		sid = ""
	case ACLSubjectAccount, ACLSubjectScope, ACLSubjectLink, ACLSubjectWorkspace, ACLSubjectWorkspaceRole:
		if sid == "" {
			return database.FileACLEntry{}, fmt.Errorf("subject_id is required for subject_type %q", st)
		}
	case ACLSubjectWorkspaceGroup:
		if !isWorkspaceFile {
			return database.FileACLEntry{}, errors.New("workspace_group ACL entries are only supported on workspace files")
		}
		if sid == "" {
			return database.FileACLEntry{}, errors.New("subject_id is required for workspace_group")
		}
	default:
		return database.FileACLEntry{}, fmt.Errorf("unsupported subject_type %q", in.SubjectType)
	}

	mask := PermissionMask(in.Permissions) & PermAll
	if mask == PermNone && strings.TrimSpace(in.Role) != "" {
		parsed, err := ParsePermissionMask(in.Role)
		if err != nil {
			return database.FileACLEntry{}, err
		}
		mask = parsed
	}
	if mask == PermNone {
		return database.FileACLEntry{}, errors.New("permissions or role is required")
	}

	inheritable := true
	if in.Inheritable != nil {
		inheritable = *in.Inheritable
	}

	return database.FileACLEntry{
		ID:          database.NewID(),
		FileID:      file.ID,
		SubjectType: st,
		SubjectID:   sid,
		Effect:      effect,
		Permissions: uint32(mask),
		Inheritable: inheritable,
		ExpiresAt:   in.ExpiresAt,
		CreatedBy:   actorID,
	}, nil
}

// ReplaceFileACL atomically replaces direct ACL entries and updates inherit_acl
// and has_acl on the target file.
func (s *FileService) ReplaceFileACL(ctx context.Context, file *database.CloudFile, inheritACL *bool, inputs []ACLEntryInput, actorID *uuid.UUID) error {
	if file == nil {
		return gorm.ErrRecordNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Deduplicate by (subject_type, subject_id, effect), merging bitmasks.
	type key struct{ st, sid, eff string }
	ordered := make([]key, 0, len(inputs))
	byKey := make(map[key]database.FileACLEntry, len(inputs))
	for _, in := range inputs {
		entry, err := normalizeACLEntryInput(file, in, actorID)
		if err != nil {
			return err
		}
		k := key{entry.SubjectType, entry.SubjectID, entry.Effect}
		if existing, ok := byKey[k]; ok {
			existing.Permissions |= entry.Permissions
			existing.Inheritable = existing.Inheritable || entry.Inheritable
			if entry.ExpiresAt != nil {
				existing.ExpiresAt = entry.ExpiresAt
			}
			byKey[k] = existing
		} else {
			ordered = append(ordered, k)
			byKey[k] = entry
		}
	}

	entries := make([]database.FileACLEntry, 0, len(ordered))
	for _, k := range ordered {
		entries = append(entries, byKey[k])
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("file_id = ?", file.ID).Delete(&database.FileACLEntry{}).Error; err != nil {
			return err
		}
		if len(entries) > 0 {
			if err := tx.Create(&entries).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{"has_acl": len(entries) > 0}
		if inheritACL != nil {
			updates["inherit_acl"] = *inheritACL
		}
		return tx.Model(&database.CloudFile{}).Where("id = ?", file.ID).Updates(updates).Error
	}); err != nil {
		return err
	}
	// Keep the caller's copy consistent: evaluations trust has_acl, so a stale
	// false would silently drop the entries just written from later checks.
	file.HasACL = len(entries) > 0
	if inheritACL != nil {
		file.InheritACL = *inheritACL
	}
	return nil
}

// UpsertFileACLEntry adds or updates a single ACL entry on file and sets has_acl.
func (s *FileService) UpsertFileACLEntry(ctx context.Context, file *database.CloudFile, in ACLEntryInput, actorID *uuid.UUID) (*database.FileACLEntry, error) {
	if file == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	entry, err := normalizeACLEntryInput(file, in, actorID)
	if err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "file_id"},
				{Name: "subject_type"},
				{Name: "subject_id"},
				{Name: "effect"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"permissions", "inheritable", "expires_at", "updated_at"}),
		}).Create(&entry).Error; err != nil {
			return err
		}
		// Reload into a fresh value: entry still carries the generated ID,
		// which GORM would append to the WHERE clause and miss the row that
		// the conflict update kept.
		var stored database.FileACLEntry
		if err := tx.Where("file_id = ? AND subject_type = ? AND subject_id = ? AND effect = ?",
			entry.FileID, entry.SubjectType, entry.SubjectID, entry.Effect).First(&stored).Error; err != nil {
			return err
		}
		entry = stored
		return tx.Model(&database.CloudFile{}).Where("id = ?", file.ID).Update("has_acl", true).Error
	}); err != nil {
		return nil, err
	}
	file.HasACL = true
	return &entry, nil
}

// DeleteFileACLEntry removes one direct ACL entry and refreshes cloud_files.has_acl.
func (s *FileService) DeleteFileACLEntry(ctx context.Context, fileID, entryID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ? AND file_id = ?", strings.TrimSpace(entryID), strings.TrimSpace(fileID)).
			Delete(&database.FileACLEntry{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		var remaining int64
		if err := tx.Model(&database.FileACLEntry{}).Where("file_id = ?", fileID).Count(&remaining).Error; err != nil {
			return err
		}
		return tx.Model(&database.CloudFile{}).Where("id = ?", fileID).Update("has_acl", remaining > 0).Error
	})
}

// SetFilePublicACL toggles anonymous public read access on file cleanly
// (without leaving conflicting rows behind).
func (s *FileService) SetFilePublicACL(ctx context.Context, fileID string, isPublic bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return gorm.ErrRecordNotFound
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("file_id = ? AND subject_type = ?", fileID, ACLSubjectPublic).
			Delete(&database.FileACLEntry{}).Error; err != nil {
			return err
		}
		entry := database.FileACLEntry{
			ID:          database.NewID(),
			FileID:      fileID,
			SubjectType: ACLSubjectPublic,
			SubjectID:   "",
			Effect:      ACLEffectAllow,
			Permissions: uint32(PermRead),
			Inheritable: true,
		}
		if !isPublic {
			entry.Effect = ACLEffectDeny
		}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		return tx.Model(&database.CloudFile{}).Where("id = ?", fileID).Update("has_acl", true).Error
	})
}

// seedSharedPoolFolderPrivacy keeps a folder tree private when its root folder
// lives in a shared (account-less) pool. The pool default publishes bare files
// and folders, so a root folder carries an explicit inheritable public-read
// deny that every descendant inherits; callers lift it by removing the entry.
func (s *FileService) seedSharedPoolFolderPrivacy(tx *gorm.DB, file *database.CloudFile) error {
	if file == nil || !file.IsFolder || file.ParentID != nil {
		return nil
	}
	if !s.isSharedPoolFile(file) {
		return nil
	}
	entry := database.FileACLEntry{
		ID:          database.NewID(),
		FileID:      file.ID,
		SubjectType: ACLSubjectPublic,
		SubjectID:   "",
		Effect:      ACLEffectDeny,
		Permissions: uint32(PermRead),
		Inheritable: true,
	}
	if err := tx.Create(&entry).Error; err != nil {
		return err
	}
	if err := tx.Model(&database.CloudFile{}).Where("id = ?", file.ID).Update("has_acl", true).Error; err != nil {
		return err
	}
	file.HasACL = true
	return nil
}

// ListSharedWithMe returns files explicitly shared with the caller's account
// (excluding files owned by the caller), paginated in SQL.
func (s *FileService) ListSharedWithMe(ctx context.Context, caller *PrincipalSet, opts FileListOptions) ([]database.CloudFile, int64, error) {
	if caller == nil || !caller.Authenticated {
		return []database.CloudFile{}, 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	subquery := s.db.WithContext(ctx).Model(&database.FileACLEntry{}).
		Select("DISTINCT file_id").
		Where("subject_type = ? AND subject_id = ? AND effect = ? AND (expires_at IS NULL OR expires_at > ?)",
			ACLSubjectAccount, caller.AccountID, ACLEffectAllow, now)

	query := s.db.WithContext(ctx).Model(&database.CloudFile{}).
		Where("cloud_files.id IN (?)", subquery).
		Where("cloud_files.account_id <> ?", caller.AccountID).
		Where("cloud_files.derived_from_id IS NULL")

	files, total, err := s.listFilesPage(query, opts)
	if err != nil {
		return nil, 0, err
	}
	if err := s.PopulateFilesMetadataForCaller(ctx, caller, files); err != nil {
		return nil, 0, err
	}
	// A grant can coexist with an explicit deny (deny wins), so only surface
	// rows the caller can still read.
	readable := make([]database.CloudFile, 0, len(files))
	for i := range files {
		if files[i].PermissionStatus.Readable {
			readable = append(readable, files[i])
		}
	}
	return readable, total, nil
}
