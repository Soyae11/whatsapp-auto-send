<?php

namespace Wa\Laravel\Enums;

enum FailureCode: string
{
    case NotOnWhatsApp = 'not_on_whatsapp';
    case SenderLoggedOut = 'sender_logged_out';
    case RateLimitedByWhatsApp = 'rate_limited_by_whatsapp';
    case SendFailed = 'send_failed';
    case Unknown = 'unknown';

    public static function parse(?string $value): self
    {
        return self::tryFrom((string) $value) ?? self::Unknown;
    }

    public function worthResending(): bool
    {
        return $this !== self::NotOnWhatsApp;
    }
}
