<?php

namespace App\Services\Baileys;

use App\Exceptions\BaileysApiException;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Support\Facades\Http;

class BaileysClient
{
    public function list(): array
    {
        return $this->request()->get('/sessions')->throw($this->mapError())->json('sessions');
    }

    public function create(string $label): array
    {
        return $this->request()->post('/sessions', ['label' => $label])
            ->throw($this->mapError())->json();
    }

    public function connect(string $id): array
    {
        return $this->request()->post("/sessions/{$id}/connect")
            ->throw($this->mapError())->json();
    }

    public function pair(string $id, string $phoneNumber): array
    {
        return $this->request()->post("/sessions/{$id}/pair", ['phoneNumber' => $phoneNumber])
            ->throw($this->mapError())->json();
    }

    public function qr(string $id): array
    {
        return $this->request()->get("/sessions/{$id}/qr")->throw($this->mapError())->json();
    }

    public function logout(string $id): array
    {
        return $this->request()->post("/sessions/{$id}/logout")
            ->throw($this->mapError())->json();
    }

    public function delete(string $id): void
    {
        $this->request()->delete("/sessions/{$id}")->throw($this->mapError());
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
