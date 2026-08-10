<?php

namespace Wa\Laravel\Webhooks;

final class Signature
{
    public const SIGNATURE_HEADER = 'X-WA-Signature';

    public const TIMESTAMP_HEADER = 'X-WA-Timestamp';

    public const EVENT_ID_HEADER = 'X-WA-Event-Id';

    public const EVENT_TYPE_HEADER = 'X-WA-Event-Type';

    public static function compute(string $secret, string $timestamp, string $rawBody): string
    {
        return 'sha256='.hash_hmac('sha256', $timestamp.'.'.$rawBody, $secret);
    }

    public static function verify(string $secret, ?string $signature, ?string $timestamp, string $rawBody, int $tolerance, ?int $now = null): bool
    {
        if ($secret === '' || ! is_string($signature) || ! is_string($timestamp) || $signature === '' || $timestamp === '') {
            return false;
        }

        if (! ctype_digit($timestamp)) {
            return false;
        }

        if (abs(($now ?? time()) - (int) $timestamp) > $tolerance) {
            return false;
        }

        return hash_equals(self::compute($secret, $timestamp, $rawBody), $signature);
    }
}
