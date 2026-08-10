<?php

namespace App\Exceptions;

use Exception;
use Illuminate\Http\Client\Response;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Inertia\Inertia;

class WaAdminApiException extends Exception
{
    public function __construct(
        public readonly string $errorCode,
        string $message,
        public readonly bool $retryable,
        public readonly int $statusCode,
    ) {
        parent::__construct($message);
    }

    public static function fromResponse(Response $response): self
    {
        $body = $response->json() ?? [];

        return new self(
            errorCode: $body['error_code'] ?? 'unknown_error',
            message: $body['message'] ?? 'wa-consumer-api\'s admin surface returned an unexpected error',
            retryable: (bool) ($body['retryable'] ?? false),
            statusCode: $response->status(),
        );
    }

    public function render(Request $request): JsonResponse|RedirectResponse
    {
        if ($request->header('X-Inertia')) {
            Inertia::flash('toast', ['type' => 'error', 'message' => $this->getMessage()]);

            return redirect()->back();
        }

        return response()->json([
            'error_code' => $this->errorCode,
            'message' => $this->getMessage(),
            'retryable' => $this->retryable,
        ], $this->statusCode);
    }
}
