<?php

namespace Wa\Laravel\Testing;

use DateTimeImmutable;
use PHPUnit\Framework\Assert;
use Wa\Laravel\BatchResult;
use Wa\Laravel\Contracts\WaClient;
use Wa\Laravel\Enums\Status;
use Wa\Laravel\Exceptions\MessageNotFound;
use Wa\Laravel\Message;
use Wa\Laravel\MessagePage;
use Wa\Laravel\OutgoingMessage;
use Wa\Laravel\PendingMessage;

final class WaFake implements WaClient
{
    private array $sent = [];

    private array $cancelled = [];

    private int $counter = 0;

    public function __construct(
        private readonly ?string $defaultSender = null,
        private readonly bool $dryRun = true,
    ) {}

    public function to(string $phone): PendingMessage
    {
        return new PendingMessage($this, $phone);
    }

    public function send(OutgoingMessage $message): Message
    {
        $message = $message->withDefaults($this->defaultSender, $this->dryRun)->validated();

        $result = $this->fakeMessage($message);

        $this->sent[] = ['outgoing' => $message, 'message' => $result];

        return $result;
    }

    public function sendMany(iterable $messages): BatchResult
    {
        $accepted = [];

        foreach ($messages as $message) {
            $accepted[] = $this->send($message instanceof PendingMessage ? $message->message() : $message);
        }

        return new BatchResult($accepted, []);
    }

    public function find(string $id): Message
    {
        foreach ($this->sent as $record) {
            if ($record['message']->id === $id) {
                return $record['message'];
            }
        }

        throw new MessageNotFound(
            'wa (fake): no message with id '.$id.' was sent in this test.',
            'message_not_found',
            404,
            false,
        );
    }

    public function messages(array $filters = []): MessagePage
    {
        $matches = array_values(array_filter(
            array_map(static fn (array $record) => $record['message'], $this->sent),
            static function (Message $message) use ($filters) {
                foreach (['reference' => $message->reference, 'sender' => $message->sender] as $key => $value) {
                    if (isset($filters[$key]) && $filters[$key] !== $value) {
                        return false;
                    }
                }

                return true;
            },
        ));

        return new MessagePage(array_reverse($matches), false, null);
    }

    public function cancel(string $id): Message
    {
        $message = $this->find($id);
        $this->cancelled[] = $id;

        return Message::fromArray(['status' => Status::Cancelled->value] + $message->raw);
    }

    public function senders(): array
    {
        return [];
    }

    public function defaultSender(): ?string
    {
        return $this->defaultSender;
    }

    public function dryRunByDefault(): bool
    {
        return $this->dryRun;
    }

    public function sent(?callable $filter = null): array
    {
        if ($filter === null) {
            return $this->sent;
        }

        return array_values(array_filter(
            $this->sent,
            static fn (array $record) => (bool) $filter($record['outgoing'], $record['message']),
        ));
    }

    public function cancelled(): array
    {
        return $this->cancelled;
    }

    public function assertSent(?callable $filter = null, ?int $times = null): self
    {
        $matches = $this->sent($filter);

        if ($times !== null) {
            Assert::assertCount($times, $matches, sprintf(
                'Expected %d matching WhatsApp message(s), found %d.', $times, count($matches),
            ));

            return $this;
        }

        Assert::assertNotEmpty($matches, 'No matching WhatsApp message was sent.'.$this->summary());

        return $this;
    }

    public function assertNotSent(?callable $filter = null): self
    {
        Assert::assertEmpty($this->sent($filter), 'A matching WhatsApp message was sent.'.$this->summary());

        return $this;
    }

    public function assertSentTo(string $phone, ?callable $filter = null): self
    {
        return $this->assertSent(static function (OutgoingMessage $outgoing, Message $message) use ($phone, $filter) {
            if (! self::sameNumber($outgoing->to, $phone) && ! self::sameNumber($message->to, $phone)) {
                return false;
            }

            return $filter === null || $filter($outgoing, $message);
        });
    }

    public function assertSentCount(int $times): self
    {
        Assert::assertCount($times, $this->sent, sprintf(
            'Expected %d WhatsApp message(s), found %d.'.$this->summary(), $times, count($this->sent),
        ));

        return $this;
    }

    public function assertNothingSent(): self
    {
        Assert::assertEmpty($this->sent, 'WhatsApp messages were sent.'.$this->summary());

        return $this;
    }

    public function assertSentWithKey(string $idempotencyKey): self
    {
        return $this->assertSent(static fn (OutgoingMessage $outgoing) => $outgoing->idempotencyKey === $idempotencyKey);
    }

    private function fakeMessage(OutgoingMessage $message): Message
    {
        $this->counter++;

        $id = ($message->dryRun ? 'msg_dry_fake_' : 'msg_fake_').str_pad((string) $this->counter, 6, '0', STR_PAD_LEFT);
        $now = new DateTimeImmutable;

        return Message::fromArray([
            'id' => $id,
            'status' => Status::Queued->value,
            'sender' => $message->sender,
            'to' => self::normalise($message->to),
            'type' => 'text',
            'priority' => $message->priority?->value ?? 'default',
            'dry_run' => (bool) $message->dryRun,
            'estimated_send_at' => $now->modify('+6 seconds')->format(DATE_RFC3339),
            'reference' => $message->reference,
            'metadata' => $message->metadata,
            'attempts' => 0,
            'timestamps' => ['queued_at' => $now->format(DATE_RFC3339)],
            'created_at' => $now->format(DATE_RFC3339),
        ]);
    }

    private static function normalise(string $phone): string
    {
        $digits = preg_replace('/\D+/', '', $phone) ?? '';

        return str_starts_with($digits, '0') ? '62'.substr($digits, 1) : $digits;
    }

    private static function sameNumber(string $a, string $b): bool
    {
        return self::normalise($a) === self::normalise($b);
    }

    private function summary(): string
    {
        if ($this->sent === []) {
            return ' Nothing was sent.';
        }

        $lines = array_map(
            static fn (array $record) => sprintf(
                "\n  - %s to %s (key %s)",
                $record['outgoing']->sender,
                $record['message']->to,
                $record['outgoing']->idempotencyKey,
            ),
            $this->sent,
        );

        return ' Sent:'.implode('', $lines);
    }
}
