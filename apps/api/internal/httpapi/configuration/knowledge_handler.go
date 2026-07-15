// Package configurationhttp defines the HTTP boundary for knowledge configuration.
package configurationhttp

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// KnowledgeHandler registers knowledge configuration placeholder endpoints.
type KnowledgeHandler struct{}

// NewKnowledgeHandler constructs a knowledge configuration HTTP handler.
// TODO(knowledge-handler-dependencies): Inject KnowledgeService only after access control,
// repositories, audit, and provider ports have tested implementations. The handler must never
// parse files, access SQL, or execute RAG directly.
func NewKnowledgeHandler() *KnowledgeHandler {
	return &KnowledgeHandler{}
}

// Name returns the module registration name.
func (h *KnowledgeHandler) Name() string {
	return "configuration"
}

// Register attaches knowledge configuration routes beneath the API version group.
func (h *KnowledgeHandler) Register(parent *gin.RouterGroup) {
	group := parent.Group("/configuration/knowledge")
	group.POST("/import-jobs", h.createImportJob)
	group.POST("/import-items/:import_item_id/reviews", h.reviewImportItem)
	group.POST("/publications", h.createPublication)
	group.GET("/bases/:knowledge_base_id/publications/:publication_id", h.getPublishedBundle)
	group.POST("/publications/:publication_id/retirements", h.retirePublication)
}

// createImportJob is the placeholder knowledge-import endpoint.
func (h *KnowledgeHandler) createImportJob(ctx *gin.Context) {
	// TODO(knowledge-create-import): Validate session, organization scope, and FORM/FILE metadata;
	// authorize the write and create the job idempotently. Cover invalid files, parser failures, and
	// provider failures. Parsing, persistence, and vectorization must remain behind injected ports.
	writeNotImplemented(ctx)
}

// reviewImportItem is the placeholder human-review endpoint.
func (h *KnowledgeHandler) reviewImportItem(ctx *gin.Context) {
	// TODO(knowledge-review-item): Require maintenance permission, validate decision and version,
	// preserve corrections and reasons, block unsafe or source-free content, audit, and test stale writes.
	writeNotImplemented(ctx)
}

// createPublication is the placeholder publication endpoint.
func (h *KnowledgeHandler) createPublication(ctx *gin.Context) {
	// TODO(knowledge-create-publication): Verify every entry is reviewed and scope-consistent, then
	// create the immutable manifest and version atomically while preserving the prior version on failure.
	writeNotImplemented(ctx)
}

// getPublishedBundle is the placeholder exact-version query endpoint.
func (h *KnowledgeHandler) getPublishedBundle(ctx *gin.Context) {
	// TODO(knowledge-get-bundle): Authorize the exact organization and publication version, return
	// immutable entries with sources, prevent existence leaks, and never perform runtime RAG retrieval.
	writeNotImplemented(ctx)
}

// retirePublication is the placeholder publication-retirement endpoint.
func (h *KnowledgeHandler) retirePublication(ctx *gin.Context) {
	// TODO(knowledge-retire-publication): Require an authorized actor and reason, retire without
	// deleting history using optimistic concurrency, audit the change, and cover retries and stale versions.
	writeNotImplemented(ctx)
}

// writeNotImplemented keeps placeholder routes visibly unimplemented.
// TODO(knowledge-api-activation): Remove this helper one operation at a time only when domain behavior,
// persistence boundaries, authorization, audit, OpenAPI success responses, and success/failure tests
// ship together. Never return fabricated jobs, entries, or publications.
func writeNotImplemented(ctx *gin.Context) {
	ctx.JSON(http.StatusNotImplemented, gin.H{
		"error": gin.H{
			"code":    "NOT_IMPLEMENTED",
			"message": "该接口尚未实现",
		},
	})
}
