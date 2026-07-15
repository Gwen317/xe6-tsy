package app

import (
	"net/http"

	"github.com/1024XEngineer/xe6-tsy/apps/api/internal/config"
	configurationhttp "github.com/1024XEngineer/xe6-tsy/apps/api/internal/httpapi/configuration"
	identityhttp "github.com/1024XEngineer/xe6-tsy/apps/api/internal/httpapi/identity"
	"github.com/1024XEngineer/xe6-tsy/apps/api/internal/httpserver"
	"github.com/1024XEngineer/xe6-tsy/apps/api/internal/modules"
)

func New(cfg config.Config) http.Handler {
	// TODO(identity-wiring): Replace the empty identity handler with explicit
	// Authenticator and identity.Service dependencies after their approved
	// implementation exists. Do not add demo credentials or an allow-by-default
	// authorization path to make the endpoint appear functional.
	//
	// TODO(knowledge-wiring): Inject configuration.KnowledgeService only after
	// authorization, repositories, audit recording, and provider boundaries have
	// tests. Until then the registered knowledge routes must keep returning 501.
	registrars := modules.Foundation(
		identityhttp.NewHandler(),
		configurationhttp.NewKnowledgeHandler(),
	)
	return httpserver.New(cfg, registrars)
}
