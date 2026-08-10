<?php

namespace Wa\Laravel\Webhooks;

use Illuminate\Contracts\Events\Dispatcher;
use Illuminate\Http\Request;
use Illuminate\Http\Response;
use Wa\Laravel\Events\MessageDelivered;
use Wa\Laravel\Events\MessageFailed;
use Wa\Laravel\Events\MessageRead;
use Wa\Laravel\Events\MessageSent;
use Wa\Laravel\Events\WebhookReceived;

final class WebhookController
{
    private const EVENTS = [
        'message.sent' => MessageSent::class,
        'message.delivered' => MessageDelivered::class,
        'message.read' => MessageRead::class,
        'message.failed' => MessageFailed::class,
    ];

    public function __construct(private readonly Dispatcher $events) {}

    public function __invoke(Request $request): Response
    {
        $event = WebhookEvent::fromRequest($request);

        $this->events->dispatch(new WebhookReceived($event));

        if ($class = self::EVENTS[$event->type] ?? null) {
            $this->events->dispatch(new $class($event));
        }

        return new Response('', 204);
    }
}
