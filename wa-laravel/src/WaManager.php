<?php

namespace Wa\Laravel;

use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\Factory;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Http\Client\Response;
use Wa\Laravel\Contracts\WaClient;
use Wa\Laravel\Exceptions\ConnectionFailed;
use Wa\Laravel\Exceptions\ProblemFactory;
use Wa\Laravel\Exceptions\UnexpectedResponse;

final class WaManager implements WaClient
{
    public function __construct(
        private readonly Factory $http,
        private readonly array $config,
    ) {}

    public function to(string $phone): PendingMessage
    {
        return new PendingMessage($this, $phone);
    }

    public function send(OutgoingMessage $message): Message
    {
        $message = $this->prepare($message);

        $response = $this->call(fn (PendingRequest $request) => $request
            ->withHeaders($this->sendHeaders($message))
            ->post('/v1/messages', $message->body()));

        return Message::fromArray(
            $this->decode($response),
            $response->header('X-Idempotent-Replay') === 'true',
        );
    }

    public function sendMany(iterable $messages): BatchResult
    {
        $prepared = [];

        foreach ($messages as $message) {
            $prepared[] = $this->prepare($message instanceof PendingMessage ? $message->message() : $message);
        }

        if ($prepared === []) {
            return new BatchResult([], []);
        }

        $dryRun = array_reduce($prepared, static fn (bool $carry, OutgoingMessage $m) => $carry || $m->dryRun, false);

        $response = $this->call(fn (PendingRequest $request) => $request
            ->withHeaders(array_filter([
                'Idempotency-Key' => $this->batchKey($prepared),
                'X-Dry-Run' => $dryRun ? 'true' : null,
            ]))
            ->post('/v1/messages/batch', [
                'messages' => array_map(static fn (OutgoingMessage $m) => $m->batchBody(), $prepared),
            ]));

        return $this->batchResult($this->decode($response));
    }

    public function find(string $id): Message
    {
        $response = $this->call(fn (PendingRequest $request) => $request->get('/v1/messages/'.rawurlencode($id)));

        return Message::fromArray($this->decode($response));
    }

    public function messages(array $filters = []): MessagePage
    {
        $response = $this->call(fn (PendingRequest $request) => $request->get('/v1/messages', $filters));

        return MessagePage::fromArray($this->decode($response));
    }

    public function cancel(string $id): Message
    {
        $response = $this->call(fn (PendingRequest $request) => $request->post('/v1/messages/'.rawurlencode($id).'/cancel'));

        return Message::fromArray($this->decode($response));
    }

    public function senders(): array
    {
        $response = $this->call(fn (PendingRequest $request) => $request->get('/v1/senders'));

        return array_map(SenderStatus::fromArray(...), $this->decode($response)['data'] ?? []);
    }

    public function defaultSender(): ?string
    {
        $sender = $this->config['senders']['default'] ?? null;

        return is_string($sender) && $sender !== '' ? $sender : null;
    }

    public function dryRunByDefault(): bool
    {
        return (bool) ($this->config['dry_run'] ?? false);
    }

    private function prepare(OutgoingMessage $message): OutgoingMessage
    {
        return $message->withDefaults($this->defaultSender(), $this->dryRunByDefault())->validated();
    }

    private function sendHeaders(OutgoingMessage $message): array
    {
        return array_filter([
            'Idempotency-Key' => $message->idempotencyKey,
            'X-Dry-Run' => $message->dryRun ? 'true' : null,
        ]);
    }

    private function batchKey(array $messages): string
    {
        $keys = array_map(static fn (OutgoingMessage $m) => $m->idempotencyKey, $messages);

        return 'batch-'.substr(hash('sha256', implode("\n", $keys)), 0, 40);
    }

    private function batchResult(array $payload): BatchResult
    {
        $accepted = [];
        $rejected = [];

        foreach ($payload['results'] ?? [] as $result) {
            if (($result['status'] ?? '') === 'accepted' && isset($result['message'])) {
                $accepted[$result['index'] ?? count($accepted)] = Message::fromArray($result['message']);

                continue;
            }

            $problem = $result['error'] ?? [];
            $rejected[$result['index'] ?? count($rejected)] = ProblemFactory::fromArray(
                is_array($problem) ? $problem : [],
                (int) ($problem['status'] ?? 400),
            );
        }

        return new BatchResult($accepted, $rejected);
    }

    private function request(): PendingRequest
    {
        return $this->http
            ->baseUrl(rtrim((string) ($this->config['url'] ?? ''), '/'))
            ->withToken((string) ($this->config['key'] ?? ''))
            ->timeout((int) ($this->config['timeout'] ?? 10))
            ->connectTimeout((int) ($this->config['connect_timeout'] ?? 5))
            ->acceptJson()
            ->asJson();
    }

    private function call(callable $callback): Response
    {
        try {
            $response = $callback($this->request());
        } catch (ConnectionException $e) {
            throw new ConnectionFailed(
                'wa: could not reach '.($this->config['url'] ?? 'the dispatcher').' ('.$e->getMessage().'). '.
                'Nothing was queued. Safe to retry with the same idempotency key.',
                'connection_failed',
                0,
                true,
                null,
                [],
                null,
                [],
            );
        }

        if ($response->failed()) {
            throw ProblemFactory::fromResponse($response);
        }

        return $response;
    }

    private function decode(Response $response): array
    {
        $decoded = $response->json();

        if (! is_array($decoded)) {
            throw new UnexpectedResponse(
                'wa: the API answered '.$response->status().' with a body that is not JSON. '.
                'Check that WA_URL points at the dispatcher rather than at a proxy or login page.',
                'unexpected_response',
                $response->status(),
                true,
                $response->header('X-Request-Id') ?: null,
                [],
                null,
                [],
            );
        }

        return $decoded;
    }
}
