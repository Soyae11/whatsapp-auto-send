<?php

namespace App\Services\WaAdmin;

use App\Exceptions\WaAdminApiException;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Support\Facades\Http;

class WaAdminClient
{
    public function pool(string $sender): array
    {
        return $this->request()->get("/internal/senders/{$sender}/pool")
            ->throw($this->mapError())->json();
    }

    public function createPool(string $sender, array $sessionIds): array
    {
        return $this->request()->post("/internal/senders/{$sender}/pool", ['sessions' => $sessionIds])
            ->throw($this->mapError())->json();
    }

    public function deletePool(string $sender): void
    {
        $this->request()->delete("/internal/senders/{$sender}/pool")
            ->throw($this->mapError());
    }

    public function addMember(string $sender, string $sessionId): array
    {
        return $this->request()->post("/internal/senders/{$sender}/pool/members", ['session_id' => $sessionId])
            ->throw($this->mapError())->json();
    }

    public function removeMember(string $sender, string $sessionId): array
    {
        return $this->request()->delete("/internal/senders/{$sender}/pool/members/{$sessionId}")
            ->throw($this->mapError())->json();
    }

    public function promote(string $sender, string $sessionId): array
    {
        return $this->request()->post("/internal/senders/{$sender}/pool/promote", ['session_id' => $sessionId])
            ->throw($this->mapError())->json();
    }

    public function reinstate(string $sender, string $sessionId): array
    {
        return $this->request()->post("/internal/senders/{$sender}/pool/members/{$sessionId}/reinstate")
            ->throw($this->mapError())->json();
    }

    protected function request(): PendingRequest
    {
        return Http::baseUrl(config('wa-admin.base_url'))
            ->withToken(config('wa-admin.admin_key'))
            ->timeout(10)
            ->connectTimeout(3)
            ->acceptJson();
    }

    protected function mapError(): callable
    {
        return fn ($response) => WaAdminApiException::fromResponse($response);
    }
}
