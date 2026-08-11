<?php

namespace Wa\Laravel\Exceptions;

use Illuminate\Http\Client\Response;

final class ProblemFactory
{
    private const MAP = [
        'invalid_request' => InvalidRequest::class,
        'invalid_phone_number' => InvalidPhoneNumber::class,
        'unknown_sender' => UnknownSender::class,
        'idempotency_key_required' => IdempotencyKeyRequired::class,
        'batch_too_large' => BatchTooLarge::class,
        'sender_not_permitted' => SenderNotPermitted::class,
        'unauthenticated' => Unauthenticated::class,
        'not_found' => NotFound::class,
        'method_not_allowed' => MethodNotAllowed::class,
        'message_not_found' => MessageNotFound::class,
        'message_not_cancellable' => MessageNotCancellable::class,
        'idempotency_key_reused_with_different_payload' => IdempotencyKeyConflict::class,
        'payload_too_large' => PayloadTooLarge::class,
        'unsupported_media_type' => UnsupportedMediaType::class,
        'rate_limited' => RateLimited::class,
        'internal_error' => InternalError::class,
        'sender_unavailable' => SenderUnavailable::class,
        'queue_horizon_exceeded' => QueueHorizonExceeded::class,
        'sender_pool_exhausted' => SenderPoolExhausted::class,
        'sender_pool_misconfigured' => SenderPoolMisconfigured::class,
    ];

    public static function fromResponse(Response $response): WaException
    {
        $problem = self::decode($response);
        $code = (string) ($problem['code'] ?? '');

        $retryAfter = $problem['retry_after'] ?? $response->header('Retry-After');
        $retryAfter = is_numeric($retryAfter) ? (int) $retryAfter : null;

        $retryable = array_key_exists('retryable', $problem)
            ? (bool) $problem['retryable']
            : $response->status() >= 500 || $response->status() === 429;

        return self::make($code, $retryable, [
            'message' => self::message($problem, $response),
            'status' => $response->status(),
            'requestId' => $problem['request_id'] ?? $response->header('X-Request-Id') ?: null,
            'fieldErrors' => $problem['errors'] ?? [],
            'retryAfter' => $retryAfter,
            'problem' => $problem,
        ]);
    }

    public static function fromArray(array $problem, int $status): WaException
    {
        $code = (string) ($problem['code'] ?? '');
        $retryAfter = is_numeric($problem['retry_after'] ?? null) ? (int) $problem['retry_after'] : null;

        return self::make($code, (bool) ($problem['retryable'] ?? false), [
            'message' => $problem['detail'] ?? $problem['title'] ?? 'wa: request rejected',
            'status' => (int) ($problem['status'] ?? $status),
            'requestId' => $problem['request_id'] ?? null,
            'fieldErrors' => $problem['errors'] ?? [],
            'retryAfter' => $retryAfter,
            'problem' => $problem,
        ]);
    }

    private static function make(string $code, bool $retryable, array $parts): WaException
    {
        $class = self::MAP[$code] ?? ($retryable ? UnexpectedResponse::class : InvalidRequest::class);

        return new $class(
            $parts['message'],
            $code !== '' ? $code : 'internal_error',
            $parts['status'],
            $retryable,
            $parts['requestId'],
            $parts['fieldErrors'],
            $parts['retryAfter'],
            $parts['problem'],
        );
    }

    private static function decode(Response $response): array
    {
        $decoded = $response->json();

        return is_array($decoded) ? $decoded : [];
    }

    private static function message(array $problem, Response $response): string
    {
        $detail = $problem['detail'] ?? $problem['title'] ?? null;

        if (is_string($detail) && $detail !== '') {
            return $detail;
        }

        return 'wa: the API answered '.$response->status().' with no problem document. '.
            'Check that WA_URL points at the dispatcher and not at a proxy or error page.';
    }
}
