<?php

namespace Wa\Laravel\Exceptions;

use RuntimeException;

class WaException extends RuntimeException
{
    public function __construct(
        string $message,
        protected string $errorCode = 'internal_error',
        protected int $status = 0,
        protected bool $retryable = false,
        protected ?string $requestId = null,
        protected array $fieldErrors = [],
        protected ?int $retryAfter = null,
        protected array $problem = [],
    ) {
        parent::__construct($message, $status);
    }

    public function errorCode(): string
    {
        return $this->errorCode;
    }

    public function status(): int
    {
        return $this->status;
    }

    public function retryable(): bool
    {
        return $this->retryable;
    }

    public function requestId(): ?string
    {
        return $this->requestId;
    }

    public function fieldErrors(): array
    {
        return $this->fieldErrors;
    }

    public function retryAfter(): ?int
    {
        return $this->retryAfter;
    }

    public function problem(): array
    {
        return $this->problem;
    }

    public function context(): array
    {
        return array_filter([
            'wa_code' => $this->errorCode,
            'wa_status' => $this->status,
            'wa_request_id' => $this->requestId,
            'wa_retry_after' => $this->retryAfter,
        ], static fn ($value) => $value !== null && $value !== 0);
    }
}
