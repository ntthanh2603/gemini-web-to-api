package providers

import (
	"context"
	"fmt"

	"go.uber.org/zap"
)

// Factory is a simple factory for creating providers
type Factory struct {
	providers map[string]Provider
}

// NewFactory creates a new provider factory
func NewFactory() *Factory {
	return &Factory{
		providers: make(map[string]Provider),
	}
}

// Register registers a provider with a name
func (f *Factory) Register(name string, provider Provider) {
	f.providers[name] = provider
}

// Get returns a provider by name
func (f *Factory) Get(name string) Provider {
	return f.providers[name]
}

// List returns all registered provider names
func (f *Factory) List() []string {
	var names []string
	for name := range f.providers {
		names = append(names, name)
	}
	return names
}

// ProviderManager manages provider instances
type ProviderManager struct {
	factory      *Factory
	log          *zap.Logger
	selectedType string
	selectedName string
}

// NewProviderManager creates a new provider manager (no concrete imports to avoid cycles)
func NewProviderManager(log *zap.Logger) *ProviderManager {
	factory := NewFactory()
	return &ProviderManager{
		factory: factory,
		log:     log,
	}
}

// Register registers a concrete provider with the manager
func (pm *ProviderManager) Register(name string, provider Provider) {
	pm.factory.Register(name, provider)
}

// SelectProvider selects a provider by type/name to be used as the active provider
func (pm *ProviderManager) SelectProvider(providerType string) error {
	provider := pm.factory.Get(providerType)
	if provider == nil {
		return fmt.Errorf("provider '%s' not found. Available providers: %v", providerType, pm.factory.List())
	}
	pm.selectedType = providerType
	pm.selectedName = provider.GetName()
	return nil
}

// GetSelectedProvider returns the currently selected active provider
func (pm *ProviderManager) GetSelectedProvider() Provider {
	if pm.selectedType == "" {
		return nil
	}
	return pm.factory.Get(pm.selectedType)
}

// GetProvider returns a provider by name
func (pm *ProviderManager) GetProvider(name string) Provider {
	return pm.factory.Get(name)
}

// ListProviders returns all registered provider names
func (pm *ProviderManager) ListProviders() []string {
	return pm.factory.List()
}

// InitAllProviders initializes all registered providers (non-blocking - logs warnings on failure)
func (pm *ProviderManager) InitAllProviders(ctx context.Context) {
	for name, provider := range pm.factory.providers {
		if err := provider.Init(ctx); err != nil {
			// For Gemini specifically, log a more detailed error since authentication issues are common
			if name == "gemini" {
				pm.log.Error("Gemini provider initialization failed - check GEMINI_COOKIES. Common issues:",
					zap.String("provider", name),
					zap.Error(err),
					zap.String("tip1", "GEMINI_COOKIES may be expired or incomplete"),
					zap.String("tip2", "GEMINI_AUTH_USER may not match the /u/<number>/ account slot"),
					zap.String("tip3", "Copy a fresh complete Cookie header from a Gemini Web Network request"))
			} else {
				pm.log.Warn("Provider initialization failed (will retry on demand)", zap.String("provider", name), zap.Error(err))
			}
			continue
		}
		pm.log.Debug("Provider initialized successfully", zap.String("provider", name))
	}
}

// CloseAllProviders closes all registered providers
func (pm *ProviderManager) CloseAllProviders() error {
	for name, provider := range pm.factory.providers {
		if err := provider.Close(); err != nil {
			pm.log.Error("Failed to close provider", zap.String("provider", name), zap.Error(err))
		}
	}
	return nil
}
