package providers

import (
	"context"

	"go.uber.org/fx"
	"go.uber.org/zap"
)

var Module = fx.Options(
	fx.Provide(NewClient),
	fx.Provide(NewProviderManager),
	fx.Invoke(RegisterProvider),
)

func RegisterProvider(lc fx.Lifecycle, pm *ProviderManager, c *Client, log *zap.Logger) {
	pm.Register("gemini", c)

	// Select Gemini as the active provider
	if err := pm.SelectProvider("gemini"); err != nil {
		log.Error("Failed to select Gemini provider", zap.Error(err))
	}

	initCtx, cancelInit := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			log.Info("Starting Gemini provider initialization in background")
			go func() {
				if err := c.Init(initCtx); err != nil {
					log.Error("Gemini provider initialization failed", zap.Error(err))
				}
			}()
			return nil
		},
		OnStop: func(context.Context) error {
			cancelInit()
			return c.Close()
		},
	})
}
