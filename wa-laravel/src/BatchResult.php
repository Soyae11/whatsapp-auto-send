<?php

namespace Wa\Laravel;

use Wa\Laravel\Exceptions\WaException;

final readonly class BatchResult
{
    public function __construct(
        public array $accepted,
        public array $rejected,
    ) {}

    public function acceptedCount(): int
    {
        return count($this->accepted);
    }

    public function rejectedCount(): int
    {
        return count($this->rejected);
    }

    public function allAccepted(): bool
    {
        return $this->rejected === [];
    }

    public function throwIfAnyRejected(): self
    {
        if ($this->rejected !== []) {
            throw reset($this->rejected);
        }

        return $this;
    }
}
