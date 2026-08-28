import yaml

with open('api/openapi.yaml', 'r') as f:
    spec = yaml.safe_load(f)

spec['paths']['/v1/snapshot'] = {
    'post': {
        'summary': 'Snapshot sync',
        'security': [{'bearerAuth': []}],
        'requestBody': {
            'required': True,
            'content': {
                'application/json': {
                    'schema': {'$ref': '#/components/schemas/SnapshotRequest'}
                }
            }
        },
        'responses': {
            '200': {
                'description': 'Snapshot response',
                'content': {
                    'application/json': {
                        'schema': {'$ref': '#/components/schemas/SnapshotResponse'}
                    }
                }
            }
        }
    }
}

spec['paths']['/v1/stats'] = {
    'get': {
        'summary': 'Get user stats',
        'security': [{'bearerAuth': []}],
        'parameters': [
            {'name': 'event', 'in': 'query', 'required': False, 'schema': {'type': 'string'}}
        ],
        'responses': {
            '200': {
                'description': 'Stats response',
                'content': {
                    'application/json': {
                        'schema': {'$ref': '#/components/schemas/StatsResponse'}
                    }
                }
            }
        }
    }
}

spec['paths']['/v1/sessions'] = {
    'get': {
        'summary': 'List sessions',
        'security': [{'bearerAuth': []}],
        'parameters': [
            {'name': 'limit', 'in': 'query', 'required': False, 'schema': {'type': 'integer'}},
            {'name': 'cursor', 'in': 'query', 'required': False, 'schema': {'type': 'string'}}
        ],
        'responses': {
            '200': {
                'description': 'Sessions response',
                'content': {
                    'application/json': {
                        'schema': {'$ref': '#/components/schemas/PaginatedSessionsResponse'}
                    }
                }
            }
        }
    }
}

spec['paths']['/v1/sessions/{id}/solves'] = {
    'get': {
        'summary': 'List solves for a session',
        'security': [{'bearerAuth': []}],
        'parameters': [
            {'name': 'id', 'in': 'path', 'required': True, 'schema': {'type': 'string', 'format': 'uuid'}},
            {'name': 'limit', 'in': 'query', 'required': False, 'schema': {'type': 'integer'}},
            {'name': 'cursor', 'in': 'query', 'required': False, 'schema': {'type': 'string'}}
        ],
        'responses': {
            '200': {
                'description': 'Solves response',
                'content': {
                    'application/json': {
                        'schema': {'$ref': '#/components/schemas/PaginatedSolvesResponse'}
                    }
                }
            }
        }
    }
}

spec['components']['schemas'].update({
    'SnapshotRequest': {
        'type': 'object',
        'required': ['device'],
        'properties': {
            'device': {'$ref': '#/components/schemas/Device'},
            'cursor': {'type': 'string'},
            'limit': {'type': 'integer'}
        }
    },
    'SnapshotResponse': {
        'type': 'object',
        'required': ['sessions', 'solves', 'next_cursor', 'has_more', 'sync_cursor'],
        'properties': {
            'sessions': {'type': 'array', 'items': {'$ref': '#/components/schemas/Session'}},
            'solves': {'type': 'array', 'items': {'$ref': '#/components/schemas/Solve'}},
            'next_cursor': {'type': 'string'},
            'has_more': {'type': 'boolean'},
            'sync_cursor': {'type': 'integer'}
        }
    },
    'StatsResponse': {
        'type': 'object',
        'properties': {
            'total_solves': {'type': 'integer'},
            'counted_solves': {'type': 'integer'},
            'dnf_solves': {'type': 'integer'},
            'best_solve_ms': {'type': 'integer'},
            'mean_solve_ms': {'type': 'integer'},
            'current_ao5_ms': {'type': 'integer'},
            'best_ao5_ms': {'type': 'integer'},
            'current_ao12_ms': {'type': 'integer'},
            'best_ao12_ms': {'type': 'integer'}
        }
    },
    'SessionSummary': {
        'type': 'object',
        'properties': {
            'id': {'type': 'string', 'format': 'uuid'},
            'name': {'type': 'string'},
            'event': {'type': 'string'},
            'kind': {'type': 'string'},
            'started_at': {'type': 'string', 'format': 'date-time'},
            'ended_at': {'type': 'string', 'format': 'date-time'},
            'archived': {'type': 'boolean'},
            'solve_count': {'type': 'integer'}
        }
    },
    'PaginatedSessionsResponse': {
        'type': 'object',
        'properties': {
            'sessions': {'type': 'array', 'items': {'$ref': '#/components/schemas/SessionSummary'}},
            'next_cursor': {'type': 'string'},
            'has_more': {'type': 'boolean'}
        }
    },
    'PaginatedSolvesResponse': {
        'type': 'object',
        'properties': {
            'solves': {'type': 'array', 'items': {'$ref': '#/components/schemas/Solve'}},
            'next_cursor': {'type': 'string'},
            'has_more': {'type': 'boolean'}
        }
    }
})

with open('api/openapi.yaml', 'w') as f:
    yaml.dump(spec, f, sort_keys=False)
