<?php

namespace Wa\Laravel\Webhooks;

use DateTimeImmutable;
use Illuminate\Http\Request;
use Wa\Laravel\Message;

final readonly class WebhookEvent
{
    public function __construct(
        public string $id,
        public string $type,
        public ?DateTimeImmutable $createdAt,
        public Message $message,
        public array $raw,
    ) {}

    public static function fromRequest(Request $request): self
    {
        $payload = $request->json()->all();

        return self::fromArray(is_array($payload) ? $payload : []);
    }

    public static function fromArray(array $payload): self
    {
        $createdAt = null;

        if (is_string($payload['created_at'] ?? null) && $payload['created_at'] !== '') {
            try {
                $createdAt = new DateTimeImmutable($payload['created_at']);
            } catch (\Exception) {
                $createdAt = null;
            }
        }

        return new self(
            id: (string) ($payload['id'] ?? ''),
            type: (string) ($payload['type'] ?? ''),
            createdAt: $createdAt,
            message: Message::fromArray($payload['data'] ?? []),
            raw: $payload,
        );
    }

    public function messageId(): string
    {
        return $this->message->id;
    }
}
