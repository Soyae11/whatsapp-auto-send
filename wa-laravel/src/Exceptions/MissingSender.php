<?php

namespace Wa\Laravel\Exceptions;

use LogicException;

final class MissingSender extends LogicException
{
    public static function make(): self
    {
        return new self(
            'This send names no sender and wa.senders.default is not set. Call ->from() '.
            'with a logical sender name, and read that name from config rather than '.
            'hardcoding it — staging should point at a dry-run-only sender.'
        );
    }
}
