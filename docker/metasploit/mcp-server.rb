# frozen_string_literal: true

require 'bundler/setup'
require 'mcp'

# 고정 이미지의 MSF 설정은 SDK의 allowed_hosts를 전달하지 않는다.
# Compose 주소만 추가하고 기존 Host·Origin 검증과 Bearer 인증은 유지한다.
module ArtexMetasploitComposeHost
  def initialize(*args, **options)
    options[:allowed_hosts] = Array(options[:allowed_hosts]) + ['metasploit:3000']
    super(*args, **options)
  end
end

MCP::Server::Transports::StreamableHTTPTransport.prepend(ArtexMetasploitComposeHost)

load File.expand_path('msfmcpd', __dir__) if $PROGRAM_NAME == __FILE__
