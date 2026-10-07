require_relative "boot"
require "rails"

module RailsAdapterFixture
  class Application < Rails::Application
    config.load_defaults 7.1
  end
end
