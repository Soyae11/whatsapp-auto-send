<?php

namespace Wa\Laravel;

final readonly class MessagePage
{
    public function __construct(
        public array $data,
        public bool $hasMore,
        public ?string $nextCursor,
    ) {}

    public static function fromArray(array $payload): self
    {
        return new self(
            data: array_map(Message::fromArray(...), $payload['data'] ?? []),
            hasMore: (bool) ($payload['has_more'] ?? false),
            nextCursor: $payload['next_cursor'] ?? null,
        );
    }

    public function isEmpty(): bool
    {
        return $this->data === [];
    }

    public function first(): ?Message
    {
        return $this->data[0] ?? null;
    }
}
