<?php

use App\Http\Controllers\ApiKeysController;
use App\Http\Controllers\MessagesController;
use App\Http\Controllers\PoolsController;
use App\Http\Controllers\SendController;
use App\Http\Controllers\SendersController;
use App\Http\Controllers\SessionsController;
use Illuminate\Support\Facades\Route;

Route::inertia('/', 'welcome')->name('home');

Route::middleware(['auth', 'verified'])->group(function () {
    Route::get('dashboard', [SessionsController::class, 'index'])->name('dashboard');
    Route::post('sessions', [SessionsController::class, 'store'])->name('sessions.store');
    Route::post('sessions/{session}/connect', [SessionsController::class, 'connect'])->name('sessions.connect');
    Route::get('sessions/{session}/qr', [SessionsController::class, 'qr'])->name('sessions.qr');
    Route::post('sessions/{session}/pair', [SessionsController::class, 'pair'])->name('sessions.pair');
    Route::post('sessions/{session}/logout', [SessionsController::class, 'logout'])->name('sessions.logout');
    Route::delete('sessions/{session}', [SessionsController::class, 'destroy'])->name('sessions.destroy');

    Route::get('send', [SendController::class, 'index'])->name('send');
    Route::post('send', [SendController::class, 'store'])->name('send.store');

    Route::get('messages', [MessagesController::class, 'index'])->name('messages');
    Route::get('messages/{id}', [MessagesController::class, 'show'])->name('messages.show');

    Route::get('pools', [PoolsController::class, 'index'])->name('pools');
    Route::post('pools', [PoolsController::class, 'store'])->name('pools.store');
    Route::delete('pools/{sender}', [PoolsController::class, 'destroy'])->name('pools.destroy');
    Route::post('pools/{sender}/members', [PoolsController::class, 'addMember'])->name('pools.members.add');
    Route::delete('pools/{sender}/members/{sessionId}', [PoolsController::class, 'removeMember'])->name('pools.members.remove');
    Route::post('pools/{sender}/promote', [PoolsController::class, 'promote'])->name('pools.promote');
    Route::post('pools/{sender}/members/{sessionId}/reinstate', [PoolsController::class, 'reinstate'])->name('pools.reinstate');

    Route::get('senders', [SendersController::class, 'index'])->name('senders');
    Route::post('senders', [SendersController::class, 'store'])->name('senders.store');
    Route::delete('senders/{sender}', [SendersController::class, 'destroy'])->name('senders.destroy');

    Route::get('api-keys', [ApiKeysController::class, 'index'])->name('api-keys');
    Route::post('api-keys', [ApiKeysController::class, 'store'])->name('api-keys.store');
    Route::post('api-keys/{id}/revoke', [ApiKeysController::class, 'revoke'])->name('api-keys.revoke');
});

require __DIR__.'/settings.php';
