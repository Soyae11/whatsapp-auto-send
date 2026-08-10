<?php

namespace Wa\Laravel\Notifications;

use Illuminate\Notifications\Notification;
use RuntimeException;
use Wa\Laravel\Contracts\WaClient;
use Wa\Laravel\Message;

final class WhatsAppChannel
{
    public function __construct(private readonly WaClient $client) {}

    public function send(mixed $notifiable, Notification $notification): ?Message
    {
        if (! method_exists($notification, 'toWhatsApp')) {
            throw new RuntimeException(sprintf(
                '%s returned "whatsapp" from via() but has no toWhatsApp() method.',
                $notification::class,
            ));
        }

        $message = $notification->toWhatsApp($notifiable);

        if (is_string($message)) {
            $message = WhatsAppMessage::make()->text($message);
        }

        if (is_array($message)) {
            $message = self::fromArray($message);
        }

        if (! $message instanceof WhatsAppMessage) {
            throw new RuntimeException(sprintf(
                '%s::toWhatsApp() must return a %s, an array, or a string.',
                $notification::class,
                WhatsAppMessage::class,
            ));
        }

        $to = $message->recipient() ?? $this->routeFor($notifiable, $notification);

        if (($to ?? '') === '') {
            throw new RuntimeException(sprintf(
                'No WhatsApp recipient for %s. Add routeNotificationForWhatsApp() to %s, or call ->to() on the message.',
                $notification::class,
                is_object($notifiable) ? $notifiable::class : gettype($notifiable),
            ));
        }

        return $this->client->send($message->toOutgoing(
            $to,
            IdempotencyKey::for($notification, $notifiable, $message->referenceValue()),
        ));
    }

    private function routeFor(mixed $notifiable, Notification $notification): ?string
    {
        if (! is_object($notifiable) || ! method_exists($notifiable, 'routeNotificationFor')) {
            return null;
        }

        $route = $notifiable->routeNotificationFor('whatsapp', $notification);

        return is_string($route) && $route !== '' ? $route : null;
    }

    private static function fromArray(array $data): WhatsAppMessage
    {
        $message = WhatsAppMessage::make()->text((string) ($data['text'] ?? ''));

        foreach (['to', 'from', 'reference', 'key'] as $method) {
            if (isset($data[$method]) && is_string($data[$method])) {
                $message->{$method}($data[$method]);
            }
        }

        if (isset($data['sender']) && is_string($data['sender'])) {
            $message->from($data['sender']);
        }

        if (isset($data['idempotency_key']) && is_string($data['idempotency_key'])) {
            $message->key($data['idempotency_key']);
        }

        if (isset($data['priority']) && is_string($data['priority'])) {
            $message->priority($data['priority']);
        }

        if (isset($data['metadata']) && is_array($data['metadata'])) {
            $message->metadata($data['metadata']);
        }

        if (isset($data['dry_run'])) {
            $message->dryRun((bool) $data['dry_run']);
        }

        return $message;
    }
}
