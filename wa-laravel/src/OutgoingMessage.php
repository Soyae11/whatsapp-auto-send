<?php

namespace Wa\Laravel;

use Wa\Laravel\Enums\Priority;
use Wa\Laravel\Exceptions\MissingIdempotencyKey;
use Wa\Laravel\Exceptions\MissingSender;

final readonly class OutgoingMessage
{
    public function __construct(
        public string $to,
        public string $text,
        public ?string $sender = null,
        public ?Priority $priority = null,
        public ?string $reference = null,
        public array $metadata = [],
        public ?string $idempotencyKey = null,
        public ?bool $dryRun = null,
    ) {}

    public function withDefaults(?string $sender, bool $dryRun): self
    {
        return new self(
            to: $this->to,
            text: $this->text,
            sender: $this->sender ?? $sender,
            priority: $this->priority,
            reference: $this->reference,
            metadata: $this->metadata,
            idempotencyKey: $this->idempotencyKey,
            dryRun: $this->dryRun ?? $dryRun,
        );
    }

    public function withIdempotencyKey(string $key): self
    {
        return new self(
            to: $this->to,
            text: $this->text,
            sender: $this->sender,
            priority: $this->priority,
            reference: $this->reference,
            metadata: $this->metadata,
            idempotencyKey: $key,
            dryRun: $this->dryRun,
        );
    }

    public function validated(): self
    {
        if (($this->sender ?? '') === '') {
            throw MissingSender::make();
        }

        if (($this->idempotencyKey ?? '') === '') {
            throw MissingIdempotencyKey::make();
        }

        return $this;
    }

    public function body(): array
    {
        return array_filter([
            'sender' => $this->sender,
            'to' => $this->to,
            'type' => 'text',
            'text' => $this->text,
            'priority' => $this->priority?->value,
            'reference' => $this->reference,
            'metadata' => $this->metadata === [] ? null : $this->metadata,
        ], static fn ($value) => $value !== null);
    }

    public function batchBody(): array
    {
        return ['idempotency_key' => $this->idempotencyKey] + $this->body();
    }
}
