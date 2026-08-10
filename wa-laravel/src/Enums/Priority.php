<?php

namespace Wa\Laravel\Enums;

enum Priority: string
{
    case Critical = 'critical';
    case Default = 'default';
    case Bulk = 'bulk';
}
