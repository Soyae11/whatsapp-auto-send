<?php

namespace Wa\Laravel\Webhooks;

use Closure;
use Illuminate\Http\Request;
use RuntimeException;
use Symfony\Component\HttpKernel\Exception\AccessDeniedHttpException;

final class VerifyWebhookSignature
{
    public function __construct(private readonly array $config) {}

    public function handle(Request $request, Closure $next): mixed
    {
        $secret = (string) ($this->config['secret'] ?? '');

        if ($secret === '') {
            throw new RuntimeException(
                'wa: WA_WEBHOOK_SECRET is not set, so no delivery can be verified. The secret '.
                'is printed once when the platform team creates the webhook; ask them to read '.
                'it back rather than re-creating it.'
            );
        }

        $verified = Signature::verify(
            $secret,
            $request->header(Signature::SIGNATURE_HEADER),
            $request->header(Signature::TIMESTAMP_HEADER),
            $request->getContent(),
            (int) ($this->config['tolerance'] ?? 300),
        );

        if (! $verified) {
            throw new AccessDeniedHttpException('wa: webhook signature is missing, stale, or does not match.');
        }

        return $next($request);
    }
}
