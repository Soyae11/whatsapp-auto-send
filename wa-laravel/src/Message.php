<?php

namespace Wa\Laravel;

use DateTimeImmutable;
use Wa\Laravel\Enums\Status;

final readonly class Message
{
    public function __construct(
        public string $id,
        public Status $status,
        public string $sender,
        public string $to,
        public string $priority,
        public bool $dryRun,
        public int $attempts,
        public ?string $reference,
        public array $metadata,
        public ?DateTimeImmutable $estimatedSendAt,
        public ?MessageError $lastError,
        public array $timestamps,
        public ?DateTimeImmutable $createdAt,
        public bool $replayed,
        public array $raw,
    ) {}

    public static function fromArray(array $data, bool $replayed = false): self
    {
        return new self(
            id: (string) ($data['id'] ?? ''),
            status: Status::parse($data['status'] ?? null),
            sender: (string) ($data['sender'] ?? ''),
            to: (string) ($data['to'] ?? ''),
            priority: (string) ($data['priority'] ?? 'default'),
            dryRun: (bool) ($data['dry_run'] ?? false),
            attempts: (int) ($data['attempts'] ?? 0),
            reference: $data['reference'] ?? null,
            metadata: $data['metadata'] ?? [],
            estimatedSendAt: self::time($data['estimated_send_at'] ?? null),
            lastError: isset($data['last_error']) ? MessageError::fromArray($data['last_error']) : null,
            timestamps: $data['timestamps'] ?? [],
            createdAt: self::time($data['created_at'] ?? null),
            replayed: $replayed,
            raw: $data,
        );
    }

    public function timestamp(string $name): ?DateTimeImmutable
    {
        return self::time($this->timestamps[$name] ?? null);
    }

    public function isQueued(): bool
    {
        return $this->status === Status::Queued;
    }

    public function hasReachedWhatsApp(): bool
    {
        return $this->status->reachedWhatsApp();
    }

    public function hasFailed(): bool
    {
        return $this->status === Status::Failed;
    }

    private static function time(mixed $value): ?DateTimeImmutable
    {
        if (! is_string($value) || $value === '') {
            return null;
        }

        try {
            return new DateTimeImmutable($value);
        } catch (\Exception) {
            return null;
        }
    }
}
