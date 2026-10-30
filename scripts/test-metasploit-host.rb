# frozen_string_literal: true

# 고정 Metasploit 컨테이너 안에서 실행한다. 네트워크 요청·모듈 실행은 없다.
# docker compose ... exec -T metasploit ruby - ./artex-mcp-server.rb < scripts/test-metasploit-host.rb
require File.expand_path(ARGV.fetch(0, '../docker/metasploit/mcp-server.rb'), __dir__)
require 'rack/mock'
require 'securerandom'

module Msf
  module MCP
  end
end
require File.expand_path('lib/msf/core/mcp/middleware/bearer_auth', Dir.pwd)

transport = MCP::Server::Transports::StreamableHTTPTransport.new(
  MCP::Server.new(name: 'compose-host-test'),
  stateless: true, enable_json_response: true, session_idle_timeout: nil
)
token = SecureRandom.hex(24)
app = Msf::MCP::Middleware::BearerAuth.new(transport, auth_token: token)
payload = JSON.generate(
  jsonrpc: '2.0', id: 1, method: 'initialize',
  params: { protocolVersion: '2025-06-18', capabilities: {},
            clientInfo: { name: 'host-regression', version: '1' } }
)

[
  ['metasploit:3000', nil, true, 200],
  ['localhost:3000', nil, true, 200],
  ['127.0.0.1:3000', nil, true, 200],
  ['[::1]:3000', nil, true, 200],
  ['unexpected.example:3000', nil, true, 403],
  ['metasploit:3001', nil, true, 403],
  ['metasploit:3000', 'http://unexpected.example', true, 403],
  ['metasploit:3000', 'http://metasploit:3000', true, 200],
  ['metasploit:3000', nil, false, 401]
].each do |host, origin, authenticated, expected|
  env = Rack::MockRequest.env_for('http://localhost:3000/', method: 'POST', input: payload)
  env['HTTP_HOST'] = host
  env['CONTENT_TYPE'] = 'application/json'
  env['HTTP_ACCEPT'] = 'application/json, text/event-stream'
  env['HTTP_ORIGIN'] = origin if origin
  env['HTTP_AUTHORIZATION'] = "Bearer #{token}" if authenticated
  status, _, body = app.call(env)
  body.close if body.respond_to?(:close)
  raise "Host/Origin/auth regression: expected #{expected}, got #{status}" unless status == expected
end
puts 'PASS: Compose/loopback Host 허용, 임의 Host·다른 포트·외부 Origin·무인증 거부 (9건)'
