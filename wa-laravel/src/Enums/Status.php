<?php

namespace Wa\Laravel\Enums;

enum Status: string
{
    case Queued = 'queued';
    case Sending = 'sending';
    case Sent = 'sent';
    case Delivered = 'delivered';
    case Read = 'read';
    case Cancelled = 'cancelled';
    case Failed = 'failed';
    case Unknown = 'unknown';

    public static function parse(?string $value): self
    {
        return self::tryFrom((string) $value) ?? self::Unknown;
    }

    public function isTerminal(): bool
    {
        return in_array($this, [self::Cancelled, self::Failed], true);
    }

    public function hasLeftTheQueue(): bool
    {
        return ! in_array($this, [self::Queued, self::Unknown], true);
    }

    public function reachedWhatsApp(): bool
    {
        return in_array($this, [self::Sent, self::Delivered, self::Read], true);
    }
}
