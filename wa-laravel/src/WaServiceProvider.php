<?php

namespace Wa\Laravel;

use Illuminate\Http\Client\Factory;
use Illuminate\Notifications\ChannelManager;
use Illuminate\Support\Facades\Route;
use Illuminate\Support\ServiceProvider;
use Wa\Laravel\Contracts\WaClient;
use Wa\Laravel\Notifications\WhatsAppChannel;
use Wa\Laravel\Webhooks\VerifyWebhookSignature;
use Wa\Laravel\Webhooks\WebhookController;
use Wa\Laravel\Webhooks\WebhookEvent;

final class WaServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        $this->mergeConfigFrom(__DIR__.'/../config/wa.php', 'wa');

        $this->app->singleton(WaClient::class, fn ($app) => new WaManager(
            $app->make(Factory::class),
            self::resolveConfig($app),
        ));

        $this->app->alias(WaClient::class, WaManager::class);

        $this->app->singleton(VerifyWebhookSignature::class, fn ($app) => new VerifyWebhookSignature(
            $app['config']->get('wa.webhooks', []),
        ));
    }

    public function boot(): void
    {
        if ($this->app->runningInConsole()) {
            $this->publishes([__DIR__.'/../config/wa.php' => $this->app->configPath('wa.php')], 'wa-config');
        }

        $this->app->resolving(ChannelManager::class, function (ChannelManager $manager, $app): void {
            $manager->extend('whatsapp', fn () => $app->make(WhatsAppChannel::class));
        });

        $this->registerRouteMacro();
    }

    public static function resolveConfig($app): array
    {
        $config = $app['config']->get('wa', []);

        if (($config['dry_run'] ?? null) === null) {
            $config['dry_run'] = ! $app->environment('production');
        }

        $config['dry_run'] = (bool) $config['dry_run'];

        return $config;
    }

    private function registerRouteMacro(): void
    {
        if (Route::hasMacro('waWebhooks')) {
            return;
        }

        Route::macro('waWebhooks', function (string $uri = 'webhooks/wa', ?callable $handler = null) {
            $action = $handler === null
                ? WebhookController::class
                : function ($request) use ($handler) {
                    $handler(WebhookEvent::fromRequest($request));

                    return response()->noContent();
                };

            return Route::post($uri, $action)
                ->middleware(VerifyWebhookSignature::class)
                ->name('wa.webhooks');
        });
    }
}
