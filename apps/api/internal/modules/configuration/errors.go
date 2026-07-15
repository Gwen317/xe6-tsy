// Package configuration defines stable domain error categories for knowledge configuration.
package configuration

import "errors"

var (
	// TODO(knowledge-errors): Keep only errors used by the service and mapped safely by the HTTP layer.
	// Never expose raw parser, storage, SQL, provider, or sensitive-content errors to API callers.
	ErrKnowledgeNotImplemented = errors.New("knowledge capability not implemented")
	ErrKnowledgeForbidden      = errors.New("knowledge action forbidden")
	ErrKnowledgeNotFound       = errors.New("knowledge resource not found")
	ErrKnowledgeConflict       = errors.New("knowledge resource conflict")
	ErrKnowledgeUnavailable    = errors.New("knowledge dependency unavailable")
)
