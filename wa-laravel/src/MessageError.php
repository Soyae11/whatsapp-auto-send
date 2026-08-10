<?php

namespace Wa\Laravel;

use DateTimeImmutable;
use Wa\Laravel\Enums\FailureCode;

final readonly class MessageError
{
    public function __construct(
        public FailureCode $code,
        public string $detail,
        public bool $retryable,
        public ?DateTimeImmutable $at,
    ) {}

    public static function fromArray(array $data): self
    {
        $at = null;

        if (is_string($data['at'] ?? null) && $data['at'] !== '') {
            try {
                $at = new DateTimeImmutable($data['at']);
            } catch (\Exception) {
                $at = null;
            }
        }

        return new self(
            code: FailureCode::parse($data['code'] ?? null),
            detail: (string) ($data['detail'] ?? ''),
            retryable: (bool) ($data['retryable'] ?? false),
            at: $at,
        );
    }

    public function worthResending(): bool
    {
        return $this->code->worthResending();
    }
}
