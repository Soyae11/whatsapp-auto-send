<?php

namespace Wa\Laravel\Facades;

use Illuminate\Support\Facades\Facade;
use Wa\Laravel\Contracts\WaClient;
use Wa\Laravel\Testing\WaFake;
use Wa\Laravel\WaServiceProvider;

final class Wa extends Facade
{
    public static function fake(): WaFake
    {
        $config = WaServiceProvider::resolveConfig(static::$app);

        static::swap($fake = new WaFake(
            $config['senders']['default'] ?? null,
            (bool) ($config['dry_run'] ?? true),
        ));

        return $fake;
    }

    protected static function getFacadeAccessor(): string
    {
        return WaClient::class;
    }
}
