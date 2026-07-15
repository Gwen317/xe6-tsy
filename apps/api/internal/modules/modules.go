package modules

import "github.com/gin-gonic/gin"

type Registrar interface {
	Name() string
	Register(*gin.RouterGroup)
}

type placeholderModule struct {
	name string
}

func (m placeholderModule) Name() string {
	return m.name
}

func (m placeholderModule) Register(parent *gin.RouterGroup) {
	// TODO(module-placeholder): Replace this empty route group with a Registrar
	// owned by the named module when that module has an approved contract and
	// tests. Do not add empty success handlers merely to make the module visible.
	parent.Group("/" + m.name)
}

func Foundation(overrides ...Registrar) []Registrar {
	// TODO(module-foundation): As each module becomes active, pass its real
	// Registrar as an override from app.New and retain this ordered list as the
	// modular-monolith inventory. Add a registration test for every activation.
	byName := make(map[string]Registrar, len(overrides))
	for _, registrar := range overrides {
		byName[registrar.Name()] = registrar
	}

	names := []string{
		"identity",
		"configuration",
		"reception",
		"transcript",
		"workflow",
		"matter",
		"knowledge",
		"record",
		"review",
		"audit",
		"telemetry",
	}
	registrars := make([]Registrar, 0, len(names))
	for _, name := range names {
		if registrar, ok := byName[name]; ok {
			registrars = append(registrars, registrar)
			continue
		}
		registrars = append(registrars, placeholderModule{name: name})
	}
	return registrars
}
