<?php

namespace App\Services\WaAdmin;

use App\Exceptions\WaAdminApiException;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Support\Facades\Http;

class WaAdminClient
{
    public function pool(string $sender, string $ownerId): array
    {
        return $this->request()->get("/internal/senders/{$sender}/pool", ['owner_id' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function createPool(string $sender, array $sessionIds, string $ownerId): array
    {
        return $this->request()->post("/internal/senders/{$sender}/pool", [
            'sessions' => $sessionIds,
            'owner_id' => $ownerId,
        ])->throw($this->mapError())->json();
    }

    public function deletePool(string $sender, string $ownerId): void
    {
        $this->request()->withQueryParameters(['owner_id' => $ownerId])
            ->delete("/internal/senders/{$sender}/pool")
            ->throw($this->mapError());
    }

    public function addMember(string $sender, string $sessionId, string $ownerId): array
    {
        return $this->request()->post("/internal/senders/{$sender}/pool/members", [
            'session_id' => $sessionId,
            'owner_id' => $ownerId,
        ])->throw($this->mapError())->json();
    }

    public function removeMember(string $sender, string $sessionId, string $ownerId): array
    {
        return $this->request()->withQueryParameters(['owner_id' => $ownerId])
            ->delete("/internal/senders/{$sender}/pool/members/{$sessionId}")
            ->throw($this->mapError())->json();
    }

    public function promote(string $sender, string $sessionId, string $ownerId): array
    {
        return $this->request()->post("/internal/senders/{$sender}/pool/promote", [
            'session_id' => $sessionId,
            'owner_id' => $ownerId,
        ])->throw($this->mapError())->json();
    }

    public function reinstate(string $sender, string $sessionId, string $ownerId): array
    {
        return $this->request()->withQueryParameters(['owner_id' => $ownerId])
            ->post("/internal/senders/{$sender}/pool/members/{$sessionId}/reinstate")
            ->throw($this->mapError())->json();
    }

    public function listSenders(string $ownerId): array
    {
        return $this->request()->get('/internal/senders', ['owner_id' => $ownerId])
            ->throw($this->mapError())->json('data');
    }

    public function createSender(string $name, string $mode, ?string $sessionId, string $ownerId): array
    {
        return $this->request()->post('/internal/senders', [
            'name' => $name,
            'mode' => $mode,
            'session_id' => $sessionId ?? '',
            'owner_id' => $ownerId,
        ])->throw($this->mapError())->json();
    }

    public function deleteSender(string $name, string $ownerId): void
    {
        $this->request()->withQueryParameters(['owner_id' => $ownerId])
            ->delete("/internal/senders/{$name}")
            ->throw($this->mapError());
    }

    public function listKeys(string $ownerId): array
    {
        return $this->request()->get('/internal/keys', ['owner_id' => $ownerId])
            ->throw($this->mapError())->json('data');
    }

    public function createKey(array $data, string $ownerId): array
    {
        return $this->request()->post('/internal/keys', [...$data, 'owner_id' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function revokeKey(string $id, string $ownerId): array
    {
        return $this->request()->withQueryParameters(['owner_id' => $ownerId])
            ->post("/internal/keys/{$id}/revoke")
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
