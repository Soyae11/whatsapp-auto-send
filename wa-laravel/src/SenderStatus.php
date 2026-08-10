<?php

namespace Wa\Laravel;

final readonly class SenderStatus
{
    public function __construct(
        public string $name,
        public string $health,
        public string $detail,
        public bool $accepting,
        public int $queueDepth,
        public ?int $estimatedDelaySeconds,
        public bool $dryRunOnly,
    ) {}

    public static function fromArray(array $data): self
    {
        return new self(
            name: (string) ($data['name'] ?? ''),
            health: (string) ($data['health'] ?? 'degraded'),
            detail: (string) ($data['detail'] ?? ''),
            accepting: (bool) ($data['accepting'] ?? true),
            queueDepth: (int) ($data['queue_depth'] ?? 0),
            estimatedDelaySeconds: isset($data['estimated_delay_seconds']) ? (int) $data['estimated_delay_seconds'] : null,
            dryRunOnly: (bool) ($data['dry_run_only'] ?? false),
        );
    }

    public function isAvailable(): bool
    {
        return $this->health === 'available';
    }
}
