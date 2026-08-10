<?php

namespace Wa\Laravel\Events;

use Wa\Laravel\Message;
use Wa\Laravel\Webhooks\WebhookEvent;

final class MessageFailed
{
    public function __construct(public readonly WebhookEvent $event) {}

    public function message(): Message
    {
        return $this->event->message;
    }

    public function messageId(): string
    {
        return $this->event->message->id;
    }

    public function reference(): ?string
    {
        return $this->event->message->reference;
    }
}
