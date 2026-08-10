<?php

namespace Wa\Laravel;

use Wa\Laravel\Contracts\WaClient;
use Wa\Laravel\Enums\Priority;

final class PendingMessage
{
    private ?string $sender = null;

    private string $text = '';

    private ?Priority $priority = null;

    private ?string $reference = null;

    private array $metadata = [];

    private ?string $idempotencyKey = null;

    private ?bool $dryRun = null;

    public function __construct(
        private readonly WaClient $client,
        private readonly string $to,
    ) {}

    public function from(?string $sender): self
    {
        $this->sender = $sender;

        return $this;
    }

    public function text(string $text): self
    {
        $this->text = $text;

        return $this;
    }

    public function priority(Priority|string $priority): self
    {
        $this->priority = $priority instanceof Priority ? $priority : Priority::from($priority);

        return $this;
    }

    public function critical(): self
    {
        return $this->priority(Priority::Critical);
    }

    public function bulk(): self
    {
        return $this->priority(Priority::Bulk);
    }

    public function reference(?string $reference): self
    {
        $this->reference = $reference;

        return $this;
    }

    public function metadata(array $metadata): self
    {
        $this->metadata = array_map(strval(...), $metadata);

        return $this;
    }

    public function key(string $idempotencyKey): self
    {
        $this->idempotencyKey = $idempotencyKey;

        return $this;
    }

    public function dryRun(bool $dryRun = true): self
    {
        $this->dryRun = $dryRun;

        return $this;
    }

    public function message(): OutgoingMessage
    {
        return new OutgoingMessage(
            to: $this->to,
            text: $this->text,
            sender: $this->sender,
            priority: $this->priority,
            reference: $this->reference,
            metadata: $this->metadata,
            idempotencyKey: $this->idempotencyKey,
            dryRun: $this->dryRun,
        );
    }

    public function send(): Message
    {
        return $this->client->send($this->message());
    }
}
