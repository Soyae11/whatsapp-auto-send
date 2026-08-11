<?php

namespace App\Services\Baileys;

use App\Exceptions\BaileysApiException;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Support\Facades\Http;

class BaileysClient
{
    public function list(string $ownerId): array
    {
        return $this->request()->get('/sessions', ['ownerId' => $ownerId])
            ->throw($this->mapError())->json('sessions');
    }

    public function create(string $label, string $ownerId): array
    {
        return $this->request()->post('/sessions', ['label' => $label, 'ownerId' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function connect(string $id, string $ownerId): array
    {
        return $this->request()->post("/sessions/{$id}/connect", ['ownerId' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function pair(string $id, string $phoneNumber, string $ownerId): array
    {
        return $this->request()->post("/sessions/{$id}/pair", ['phoneNumber' => $phoneNumber, 'ownerId' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function qr(string $id, string $ownerId): array
    {
        return $this->request()->get("/sessions/{$id}/qr", ['ownerId' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function logout(string $id, string $ownerId): array
    {
        return $this->request()->post("/sessions/{$id}/logout", ['ownerId' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function reset(string $id, string $ownerId): array
    {
        return $this->request()->post("/sessions/{$id}/reset", ['ownerId' => $ownerId])
            ->throw($this->mapError())->json();
    }

    public function delete(string $id, string $ownerId): void
    {
        $this->request()->delete("/sessions/{$id}", ['ownerId' => $ownerId])->throw($this->mapError());
    }

    protected function request(): PendingRequest
    {
        return Http::baseUrl(config('baileys.base_url'))
            ->withToken(config('baileys.console_key'))
            ->timeout(10)
            ->connectTimeout(3)
            ->acceptJson();
    }

    protected function mapError(): callable
    {
        return fn ($response) => BaileysApiException::fromResponse($response);
    }
}
