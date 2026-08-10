<?php

namespace Wa\Laravel\Notifications;

use Illuminate\Notifications\Notification;

final class IdempotencyKey
{
    private const MAX_LENGTH = 255;

    public static function for(Notification $notification, mixed $notifiable, ?string $reference): string
    {
        $parts = [
            self::shortClass($notification::class),
            $reference !== null && $reference !== '' ? $reference : (string) ($notification->id ?? ''),
            self::notifiableKey($notifiable),
        ];

        $key = self::sanitise(implode('-', array_filter($parts, static fn (string $part) => $part !== '')));

        if ($key === '') {
            return 'wa-'.hash('sha256', $notification::class.self::notifiableKey($notifiable));
        }

        if (strlen($key) <= self::MAX_LENGTH) {
            return $key;
        }

        return substr($key, 0, self::MAX_LENGTH - 41).'-'.substr(hash('sha256', $key), 0, 40);
    }

    private static function shortClass(string $class): string
    {
        $position = strrpos($class, '\\');

        return $position === false ? $class : substr($class, $position + 1);
    }

    private static function notifiableKey(mixed $notifiable): string
    {
        if (is_object($notifiable) && method_exists($notifiable, 'getKey')) {
            return (string) $notifiable->getKey();
        }

        if (is_object($notifiable) && property_exists($notifiable, 'id')) {
            return (string) $notifiable->id;
        }

        if (is_scalar($notifiable)) {
            return (string) $notifiable;
        }

        return '';
    }

    private static function sanitise(string $value): string
    {
        $value = preg_replace('/[^\x21-\x7E]+/', '-', $value) ?? '';

        return trim($value, '-');
    }
}
