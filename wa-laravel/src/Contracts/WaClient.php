<?php

namespace Wa\Laravel\Contracts;

use Wa\Laravel\BatchResult;
use Wa\Laravel\Message;
use Wa\Laravel\MessagePage;
use Wa\Laravel\OutgoingMessage;
use Wa\Laravel\PendingMessage;

interface WaClient
{
    public function to(string $phone): PendingMessage;

    public function send(OutgoingMessage $message): Message;

    public function sendMany(iterable $messages): BatchResult;

    public function find(string $id): Message;

    public function messages(array $filters = []): MessagePage;

    public function cancel(string $id): Message;

    public function senders(): array;

    public function defaultSender(): ?string;

    public function dryRunByDefault(): bool;
}
