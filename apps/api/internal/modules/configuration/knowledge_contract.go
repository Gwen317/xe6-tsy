// Package configuration defines organization configuration contracts.
package configuration

import (
	"context"

	"github.com/1024XEngineer/xe6-tsy/apps/api/internal/modules/identity"
)

type KnowledgeService interface {
	// Methods remain contracts only until the corresponding domain implementation is delivered.
	// TODO(knowledge-create-import-job): Authorize the actor, validate input mode and provenance,
	// create the job idempotently, and delegate parsing and storage through ports. Never publish or vectorize unreviewed candidates.
	CreateKnowledgeImportJob(context.Context, CreateKnowledgeImportJobCommand) (KnowledgeImportResult, error)

	// TODO(knowledge-review-import-item): Persist actor, reason, corrections, and provenance with optimistic concurrency.
	// Reject source-free, sensitive, or version-conflicting candidates.
	ReviewKnowledgeImportItem(context.Context, ReviewKnowledgeImportItemCommand) error

	// TODO(knowledge-create-publication): Atomically create an immutable manifest and version; roll back fully on failure.
	// Never publish entries that have not completed human review.
	CreateKnowledgePublication(context.Context, CreateKnowledgePublicationCommand) (PublishedKnowledgeBaseRef, error)

	// TODO(knowledge-get-published-bundle): Authorize organization scope and return immutable entries with source references.
	GetPublishedKnowledgeBundle(context.Context, GetPublishedKnowledgeBundleQuery) (PublishedKnowledgeBundle, error)

	// TODO(knowledge-retire-publication): Authorize and audit retirement with optimistic concurrency without deleting history.
	RetirePublication(context.Context, RetirePublicationCommand) error
}

type KnowledgeInputMode string

const (
	KnowledgeInputForm KnowledgeInputMode = "FORM"
	KnowledgeInputFile KnowledgeInputMode = "FILE"
)

type CreateKnowledgeImportJobCommand struct {
	Actor           identity.AccessContext
	OrganizationID  string
	KnowledgeBaseID string
	InputMode       KnowledgeInputMode
	SourceFileRef   string
	SourceSHA256    string
	IdempotencyKey  string
}

type ReviewKnowledgeImportItemCommand struct {
	Actor            identity.AccessContext
	ImportItemID     string
	Decision         string
	ReasonCode       string
	CorrectedPayload map[string]any
	ExpectedVersion  int64
}

type CreateKnowledgePublicationCommand struct {
	Actor           identity.AccessContext
	OrganizationID  string
	KnowledgeBaseID string
	EntryVersionIDs []string
	IdempotencyKey  string
}

type GetPublishedKnowledgeBundleQuery struct {
	OrganizationID  string
	KnowledgeBaseID string
	PublicationID   string
	VersionNumber   int
}

type RetirePublicationCommand struct {
	Actor           identity.AccessContext
	PublicationID   string
	Reason          string
	ExpectedVersion int64
}

type KnowledgeImportResult struct {
	ImportJobID     string
	Status          string
	AcceptedCount   int
	WarningCount    int
	RejectedCount   int
	ValidationItems []ValidationItem
}

type ValidationItem struct {
	Severity  string
	Code      string
	FieldPath string
	Message   string
}

type PublishedKnowledgeBaseRef struct {
	OrganizationID           string
	KnowledgeBaseID          string
	PublicationID            string
	PublicationVersionNumber int
	ManifestHash             string
	ApplicableScope          map[string]any
}

type PublishedKnowledgeBundle struct {
	Publication PublishedKnowledgeBaseRef
	Entries     []PublishedKnowledgeEntry
}

type PublishedKnowledgeEntry struct {
	EntryVersionID string
	MatterName     string
	Content        map[string]any
	SourceRefs     []string
}
