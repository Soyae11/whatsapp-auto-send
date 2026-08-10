<?php

namespace Wa\Laravel\Exceptions;

use LogicException;

final class MissingIdempotencyKey extends LogicException
{
    public static function make(): self
    {
        return new self(
            'This send has no idempotency key. Call ->key() with something stable in your '.
            'domain, such as "leave-8842-notify-approver". Do not use a UUID generated at '.
            'call time: a queue retry would generate a different one and message the '.
            'recipient twice. Notifications get a key derived from the notification and '.
            'the notifiable automatically.'
        );
    }
}
