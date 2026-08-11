<?php

namespace Tests\Feature;

use App\Models\User;
use App\Services\Baileys\BaileysClient;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Inertia\Testing\AssertableInertia as Assert;
use Tests\TestCase;
use Wa\Laravel\Contracts\WaClient;

class MessagesTest extends TestCase
{
    use RefreshDatabase;

    public function test_guests_are_redirected_to_the_login_page()
    {
        $response = $this->get(route('messages'));
        $response->assertRedirect(route('login'));
    }

    public function test_messages_page_renders_sessions_from_baileys()
    {
        $user = User::factory()->create();
        $this->actingAs($user);

        $this->mock(WaClient::class, function ($mock) {
            $mock->shouldReceive('senders')->andReturn([]);
        });

        $this->mock(BaileysClient::class, function ($mock) use ($user) {
            $mock->shouldReceive('list')
                ->with((string) $user->id)
                ->andReturn([
                    ['id' => 'session_abc', 'label' => 'Warehouse', 'phoneNumber' => '+62812xxxxxxx'],
                ]);
        });

        $response = $this->get(route('messages'));

        $response->assertOk();
        $response->assertInertia(fn (Assert $page) => $page
            ->component('messages')
            ->has('sessions', 1)
            ->where('sessions.0.id', 'session_abc')
            ->where('sessions.0.label', 'Warehouse')
            ->where('sessions.0.phoneNumber', '+62812xxxxxxx')
        );
    }
}
